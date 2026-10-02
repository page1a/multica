package handler

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/titling"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Chat recap (DENE-1037): after every finished agent reply the chat's goal
// title and progress subtitle are refreshed.
//
//   - Title: named once after the first reply, from the opening message plus
//     how the agent understood it (and any linked issue), then re-checked for
//     topic drift every chatTopicCheckEvery replies. A title the user renamed
//     is locked (title_locked) and never touched; the write is also a CAS
//     against the title read here, so a rename landing mid-recap wins.
//   - Progress: the agent's own `multica chat progress` line for this turn
//     wins; otherwise a model summary of the latest turns, or — with no model
//     configured — the first line of the reply itself.
//
// Everything is best-effort on a detached goroutine; the reply is already
// delivered.

const (
	chatRecapTimeout     = 30 * time.Second
	chatRecapRecentTurns = 8
	chatTopicCheckEvery  = 4
	chatReplyProgressMax = 80
)

// RegisterChatRecap subscribes the recap to chat:done. chat:done is
// published on the in-process bus of the instance that completed the task,
// so each reply is recapped exactly once.
func (h *Handler) RegisterChatRecap(bus *events.Bus) {
	if bus == nil {
		return
	}
	bus.Subscribe(protocol.EventChatDone, func(e events.Event) {
		payload, ok := e.Payload.(protocol.ChatDonePayload)
		if !ok || payload.ChatSessionID == "" {
			return
		}
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("chat recap panicked", "session_id", payload.ChatSessionID, "panic", rec)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), chatRecapTimeout)
			defer cancel()
			if _, err := h.recapChatSession(ctx, e.WorkspaceID, parseUUID(payload.ChatSessionID)); err != nil {
				slog.Warn("chat recap failed", "session_id", payload.ChatSessionID, "error", err)
			}
		}()
	})
}

// recapChatSession runs one recap synchronously. It reports whether the
// title or progress changed (and was published).
func (h *Handler) recapChatSession(ctx context.Context, workspaceID string, sessionID pgtype.UUID) (bool, error) {
	session, err := h.Queries.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: sessionID, WorkspaceID: parseUUID(workspaceID)})
	if err != nil {
		return false, err
	}
	rows, err := h.Queries.ListChatRecapMessages(ctx, db.ListChatRecapMessagesParams{ChatSessionID: sessionID, RecentLimit: chatRecapRecentTurns})
	if err != nil {
		return false, err
	}
	if len(rows) < 2 || rows[0].Role != "user" {
		return false, nil
	}
	opening := rows[0].Content
	recent := rows[1:]
	// recent is newest first; turns are oldest first for the prompts.
	turns := make([]titling.RecapTurn, 0, len(recent))
	var lastReply string
	var lastUserAt pgtype.Timestamptz
	for i := len(recent) - 1; i >= 0; i-- {
		row := recent[i]
		if row.FailureReason.Valid || row.MessageKind == "no_response" || strings.TrimSpace(row.Content) == "" {
			continue
		}
		turns = append(turns, titling.RecapTurn{Role: row.Role, Content: row.Content})
	}
	for _, row := range recent {
		if row.Role == "assistant" && lastReply == "" && !row.FailureReason.Valid && row.MessageKind != "no_response" {
			lastReply = row.Content
		}
		if row.Role == "user" && !lastUserAt.Valid {
			lastUserAt = row.CreatedAt
		}
	}
	if strings.TrimSpace(lastReply) == "" {
		return false, nil
	}

	changed := false
	if !session.TitleLocked && h.LLM != nil && h.LLM.Enabled() {
		replies, err := h.Queries.CountChatAssistantReplies(ctx, sessionID)
		if err != nil {
			return false, err
		}
		var title string
		switch {
		case replies == 1:
			title, err = h.recapFirstTitle(ctx, workspaceID, session, opening, lastReply)
		case replies > 1 && replies%chatTopicCheckEvery == 0:
			title, err = h.recapTopicTitle(ctx, session.Title, turns)
		}
		if err != nil {
			slog.Warn("chat recap title failed; keeping title", "session_id", uuidToString(sessionID), "error", err)
		} else if title != "" && title != session.Title {
			updated, err := h.Queries.UpdateChatSessionTitleIfCurrent(ctx, db.UpdateChatSessionTitleIfCurrentParams{ID: sessionID, ExpectedTitle: session.Title, NewTitle: title})
			if err == nil {
				session, changed = updated, true
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return false, err
			}
		}
	}

	text, source := h.recapProgress(ctx, session.Title, turns, lastReply)
	if text != "" && !(session.ProgressText == text && session.ProgressSource == source) {
		updated, wrote, err := h.recordChatProgress(ctx, session, progressEntry{
			Text: text, Source: source, Tone: progress.ToneWorking, AuthorType: "system",
		}, true, lastUserAt)
		if err != nil {
			return changed, err
		}
		if wrote {
			session, changed = updated, true
		}
	}
	if changed {
		h.publishChatSessionState(workspaceID, "system", "", session)
	}
	return changed, nil
}

func (h *Handler) recapFirstTitle(ctx context.Context, workspaceID string, session db.ChatSession, opening, reply string) (string, error) {
	prompt := titling.ChatRecapTitleUserPrompt(h.chatTitleProjectNames(ctx, workspaceID, session.ID), opening, reply, h.chatLinkedIssues(ctx, workspaceID, session, opening))
	raw, err := h.LLM.GenerateText(ctx, "", titling.ChatRecapTitleSystemPrompt, prompt)
	if err != nil {
		return "", err
	}
	return sanitizeChatTitle(raw), nil
}

func (h *Handler) recapTopicTitle(ctx context.Context, current string, turns []titling.RecapTurn) (string, error) {
	raw, err := h.LLM.GenerateText(ctx, "", titling.ChatTopicSystemPrompt, titling.ChatTopicUserPrompt(current, turns))
	if err != nil {
		return "", err
	}
	title := sanitizeChatTitle(raw)
	if strings.EqualFold(title, titling.TopicKeep) {
		return "", nil
	}
	return title, nil
}

// recapProgress returns the model's progress line, or the reply's first line
// when no model is configured or the call fails.
func (h *Handler) recapProgress(ctx context.Context, title string, turns []titling.RecapTurn, reply string) (string, string) {
	if h.LLM != nil && h.LLM.Enabled() {
		raw, err := h.LLM.GenerateText(ctx, "", titling.ChatProgressSystemPrompt, titling.ChatProgressUserPrompt(title, turns))
		if err == nil {
			if text := progress.Clip(sanitizeChatTitle(raw)); text != "" {
				return text, progress.SourceModel
			}
		} else {
			slog.Warn("chat recap progress failed; using reply line", "error", err)
		}
	}
	return replyProgressLine(reply), progress.SourceReply
}

var (
	markdownLead   = regexp.MustCompile(`^(#{1,6}\s+|[-*+]\s+|>\s*|\d+[.)]\s+)`)
	markdownInline = strings.NewReplacer("**", "", "__", "", "`", "")
)

// replyProgressLine is the no-model fallback: the reply's first prose line —
// code blocks and tables skipped, Markdown markers dropped — clipped to a
// subtitle length.
func replyProgressLine(reply string) string {
	inFence := false
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "|") {
			continue
		}
		line = strings.TrimSpace(markdownInline.Replace(markdownLead.ReplaceAllString(line, "")))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > chatReplyProgressMax {
			line = strings.TrimSpace(string(r[:chatReplyProgressMax])) + "…"
		}
		return line
	}
	return ""
}

var (
	linkedIssueUUID = regexp.MustCompile(`(?:mention://issue/|/issues/)([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
	linkedIssueKey  = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9]*)-(\d+)\b`)
)

const chatLinkedIssueMax = 3

// chatLinkedIssues resolves the issues an opening message points at — a
// mention or issue URL, or a bare key like DENE-12 — so a "接管这个：<链接>"
// opening is named after the issue. Only issues the chat's creator can see
// count, and a chat shared beyond its creator only names workspace-visible
// issues: the title is shown to everyone who can see the chat.
func (h *Handler) chatLinkedIssues(ctx context.Context, workspaceID string, session db.ChatSession, opening string) []titling.LinkedIssue {
	ws := parseUUID(workspaceID)
	viewer, err := h.visibilityViewerForUser(ctx, ws, session.CreatorID)
	if err != nil {
		return nil
	}
	prefix := h.getIssuePrefix(ctx, ws)
	seen := map[string]bool{}
	var out []titling.LinkedIssue
	add := func(issue db.Issue, err error) {
		if err != nil || seen[uuidToString(issue.ID)] || len(out) >= chatLinkedIssueMax || !viewer.canSeeIssue(issue) {
			return
		}
		if session.Visibility != "private" && issue.Visibility != "workspace" {
			return
		}
		seen[uuidToString(issue.ID)] = true
		out = append(out, titling.LinkedIssue{Identifier: issueToResponse(issue, prefix).Identifier, Title: issue.Title})
	}
	for _, m := range linkedIssueUUID.FindAllStringSubmatch(opening, -1) {
		add(h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: parseUUID(m[1]), WorkspaceID: ws}))
	}
	if prefix != "" {
		for _, m := range linkedIssueKey.FindAllStringSubmatch(opening, -1) {
			if !strings.EqualFold(m[1], prefix) {
				continue
			}
			n, err := strconv.ParseInt(m[2], 10, 32)
			if err != nil {
				continue
			}
			add(h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)}))
		}
	}
	return out
}
