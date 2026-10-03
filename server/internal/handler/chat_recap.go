package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattitle"
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
	namingSource := h.chatNamingSource(ctx, session)
	if !session.TitleLocked {
		replies, err := h.Queries.CountChatAssistantReplies(ctx, sessionID)
		if err != nil {
			return false, err
		}
		var title string
		titleSource := ""
		switch {
		case replies == 1 && namingSource == "server_llm" && h.LLM != nil && h.LLM.Enabled():
			titleSource = "server_llm"
			title, err = h.recapFirstTitle(ctx, workspaceID, session, opening, lastReply)
		case replies > 1 && replies%chatTopicCheckEvery == 0 && namingSource == "server_llm" && h.LLM != nil && h.LLM.Enabled():
			titleSource = "server_llm"
			title, err = h.recapTopicTitle(ctx, session.Title, turns)
		case replies == 1 && namingSource == "runtime" && h.runtimeChatTitleReported(ctx, session):
			// The runtime has already supplied the authoritative title for this
			// opening turn. The lexical fallback must never overwrite it.
		case replies == 1 && openingStillTitles(session.Title, opening):
			titleSource = "rules"
			// Self-hosted instances without MULTICA_LLM_* still get a useful
			// title. This is deliberately lexical cleanup, never another model
			// call, and the CAS below keeps a concurrent manual rename winning.
			title = ruleCleanChatTitle(opening)
		}
		if err != nil {
			if titleSource != "" {
				h.recordChatNamingEvent(ctx, session, titleSource, "failure")
			}
			slog.Warn("chat recap title failed; keeping title", "session_id", uuidToString(sessionID), "error", err)
		} else if title != "" && title != session.Title {
			updated, err := h.Queries.UpdateChatSessionTitleIfCurrent(ctx, db.UpdateChatSessionTitleIfCurrentParams{ID: sessionID, ExpectedTitle: session.Title, NewTitle: title})
			if err == nil {
				session, changed = updated, true
				if titleSource != "" {
					h.recordChatNamingEvent(ctx, session, titleSource, "success")
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return false, err
			}
		} else if titleSource != "" {
			status := "success"
			if title == "" {
				status = "failure"
			}
			h.recordChatNamingEvent(ctx, session, titleSource, status)
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

func (h *Handler) runtimeChatTitleReported(ctx context.Context, session db.ChatSession) bool {
	if h.DB == nil {
		return false
	}
	var reported bool
	if err := h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_naming_event WHERE chat_session_id = $1 AND source = 'runtime' AND status = 'success')`, session.ID).Scan(&reported); err != nil {
		slog.Warn("check runtime chat title failed", "session_id", uuidToString(session.ID), "error", err)
		return false
	}
	return reported
}

func (h *Handler) chatNamingSource(ctx context.Context, session db.ChatSession) string {
	source := "runtime"
	if h.LLM != nil && h.LLM.Enabled() {
		source = "server_llm"
	}
	workspace, err := h.Queries.GetWorkspace(ctx, session.WorkspaceID)
	if err != nil {
		return source
	}
	var settings map[string]any
	if json.Unmarshal(workspace.Settings, &settings) == nil {
		if naming, ok := settings["naming"].(map[string]any); ok {
			if configured, ok := naming["source"].(string); ok && configured != "" {
				return configured
			}
		}
	}
	return source
}

// openingStillTitles reports whether the chat still carries the placeholder
// the first send wrote: empty, the raw opening, or chattitle.Derive of it
// (what service.SendChatMessage actually stores — the raw-opening check alone
// never matched a multi-line or long first message, so rules never ran).
func openingStillTitles(title, opening string) bool {
	title = strings.TrimSpace(title)
	return title == "" || title == strings.TrimSpace(opening) || title == chattitle.Derive(opening)
}

// openingTitleSource drops lines that are only an image/file embed so a
// screenshot pasted ahead of the question does not become the title.
func openingTitleSource(opening string) string {
	kept := make([]string, 0, strings.Count(opening, "\n")+1)
	for _, line := range strings.Split(opening, "\n") {
		if strings.TrimSpace(markdownEmbedOnly.ReplaceAllString(line, "")) == "" && strings.TrimSpace(line) != "" {
			continue
		}
		kept = append(kept, line)
	}
	if text := strings.TrimSpace(strings.Join(kept, "\n")); text != "" {
		return text
	}
	return opening
}

var markdownEmbedOnly = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

func ruleCleanChatTitle(opening string) string {
	// Derive caps at 30 runes, so drop the trailing sentence punctuation of the
	// line it will keep first; otherwise a question one "？" over the cap gets
	// truncated with "…" instead of fitting.
	source := openingTitleSource(opening)
	for _, line := range strings.Split(source, "\n") {
		if strings.TrimSpace(line) != "" {
			source = strings.TrimRight(strings.TrimSpace(line), ".。!！?？,，;；:：、 ")
			break
		}
	}
	s := chattitle.Derive(source)
	for {
		before := s
		for _, prefix := range []string{"嗯", "呃", "额", "请"} {
			if strings.HasPrefix(s, prefix) {
				s = strings.TrimSpace(s[len(prefix):])
				break
			}
		}
		s = stripEnglishChatLead(s)
		if s == before {
			break
		}
	}
	if linkedIssueKey.MatchString(s) {
		if match := linkedIssueKey.FindString(s); match != "" {
			return sanitizeChatTitle(match + " · 任务讨论")
		}
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "链接 · 任务讨论"
	}
	return sanitizeChatTitle(s)
}

func stripEnglishChatLead(s string) string {
	for _, prefix := range []string{"um", "uh", "so", "please"} {
		if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
			continue
		}
		rest := s[len(prefix):]
		if rest == "" {
			return ""
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		// Conversational leads are often followed by a comma ("So, fix …").
		// Remove that separator as part of the lead, while keeping punctuation
		// inside a real word (for example "so-called") intact.
		trimmed := strings.TrimSpace(rest)
		if trimmed != "" && strings.ContainsRune(",;:!?", []rune(trimmed)[0]) {
			return strings.TrimSpace(trimmed[1:])
		}
		return trimmed
	}
	return s
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
