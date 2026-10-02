package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ChannelChatStarted publishes list invalidation metadata without changing any
// client's current navigation. The explicit empty Chat is already committed.
func (h *Handler) ChannelChatStarted(event engine.ChannelChatStartedEvent) {
	ws, user, session := uuidToString(event.WorkspaceID), uuidToString(event.CreatorID), uuidToString(event.SessionID)
	h.publishChat(protocol.EventChatSessionCreated, ws, "member", user, session, protocol.ChatSessionCreatedPayload{
		WorkspaceID: ws, ChatSessionID: session, AgentID: uuidToString(event.AgentID), CreatorID: user, Title: event.Title,
		ChannelSource: protocol.ChatSessionChannelSource{
			ChannelType: string(event.ChannelType), InstallationID: uuidToString(event.InstallationID), RouteRevision: event.RouteRevision,
		},
		IsCurrentChannelRoute: true,
	})
}

// ChannelChatTitleInitialized publishes the deterministic first-message or
// media fallback immediately. LLM title generation is optional, so clients
// must not depend on its later CAS update to observe the committed title.
func (h *Handler) ChannelChatTitleInitialized(workspaceID, creatorID, sessionID pgtype.UUID, title string) {
	h.publishChat(protocol.EventChatSessionUpdated, uuidToString(workspaceID), "member", uuidToString(creatorID), uuidToString(sessionID), protocol.ChatSessionUpdatedPayload{
		ChatSessionID: uuidToString(sessionID),
		Title:         title,
	})
}

// GenerateChannelChatTitle is the channel engine's first-message naming
// hook. Naming now waits for the agent's first reply (DENE-1037): the
// chat:done recap in chat_recap.go names channel and first-party chats the
// same way, so this hook has nothing left to do. It stays on the lifecycle
// interface so the engine's call sites need no change.
func (h *Handler) GenerateChannelChatTitle(_, _, _ pgtype.UUID, _, _ string) {}

// chatTitleProjectNames loads the session's project display names for the
// title prompt. A lookup failure leaves the title unscoped rather than
// failing generation — the original title still stands if the model call
// itself fails, and a missing project just omits that segment.
func (h *Handler) chatTitleProjectNames(ctx context.Context, workspaceID string, sessionID pgtype.UUID) []string {
	if h.Queries == nil || strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	projects, err := h.Queries.ListChatSessionProjectsInWorkspace(ctx, db.ListChatSessionProjectsInWorkspaceParams{
		ChatSessionID: sessionID,
		WorkspaceID:   parseUUID(workspaceID),
	})
	if err != nil {
		slog.Warn("chat title project lookup failed; titling without a project",
			"session_id", uuidToString(sessionID),
			"error", err,
		)
		return nil
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		name := strings.TrimSpace(project.Title)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// chatTitleLabelPrefixes are leading labels a model sometimes prepends despite
// the prompt. Matched case-insensitively and only at the very start.
var chatTitleLabelPrefixes = []string{
	"title:", "title：",
	"标题:", "标题：",
	"题目:", "题目：",
	"主题:", "主题：",
}

// sanitizeChatTitle defensively enforces the title formatting rules regardless
// of how well the model followed the prompt: collapse whitespace, strip a
// leading "Title:" / "标题：" style label, remove surrounding quote/bracket
// pairs, drop trailing sentence punctuation, and hard-cap the length at
// chatSessionTitleMaxLen runes (the same ceiling the manual rename endpoint
// enforces). Returns "" when nothing meaningful remains.
//
// The "{project} · {topic}" shape survives this: the middle dot is not a
// wrapper and not trailing punctuation. Label prefixes and trailing periods
// are still removed.
//
// Prefix-stripping, wrapper-stripping, AND trailing-punctuation trimming all
// run in a single loop until the string stops changing. They interact: a model
// may nest them (e.g. `"Title: Fix login".` or `「标题：修复登录问题」。`), where a
// trailing `.` / `。` keeps the closing wrapper from being recognized, which in
// turn hides the forbidden prefix. Iterating all three to a fixed point peels
// them in any order.
func sanitizeChatTitle(raw string) string {
	// Collapse all internal whitespace (including newlines/tabs the model may
	// emit) into single spaces so a multi-line reply becomes one clean line.
	s := strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
	if s == "" {
		return ""
	}

	// Alternate stripping the leading label prefix, one layer of surrounding
	// quotes/brackets, and trailing sentence punctuation until none of them
	// changes anything. Bounded by the string only ever getting shorter, so it
	// always terminates.
	for {
		before := s
		s = strings.TrimSpace(stripChatTitleLabelPrefix(s))
		s = stripSurroundingQuotes(s)
		// ASCII + common CJK/full-width sentence punctuation. Trailing trim is
		// inside the loop so removing a trailing "." / "。" re-exposes a closing
		// wrapper (and the prefix it hides) for the next pass.
		s = strings.TrimSpace(strings.TrimRight(s, ".。!！?？,，;；:：、 "))
		if s == before || s == "" {
			break
		}
	}
	if s == "" {
		return ""
	}

	// Hard cap on rune length to match the manual-rename ceiling.
	if runes := []rune(s); len(runes) > chatSessionTitleMaxLen {
		s = strings.TrimSpace(string(runes[:chatSessionTitleMaxLen]))
	}
	return s
}

// stripChatTitleLabelPrefix removes one leading label prefix ("Title:",
// "标题：", ...) when present, matched case-insensitively. Returns s unchanged
// when none matches.
func stripChatTitleLabelPrefix(s string) string {
	lower := strings.ToLower(s)
	for _, p := range chatTitleLabelPrefixes {
		if strings.HasPrefix(lower, p) {
			return s[len(p):]
		}
	}
	return s
}

// chatTitleQuotePairs maps an opening quote/bracket to its closing partner.
var chatTitleQuotePairs = map[rune]rune{
	'"':  '"',
	'\'': '\'',
	'`':  '`',
	'“':  '”',
	'‘':  '’',
	'「':  '」',
	'『':  '』',
	'《':  '》',
	'（':  '）',
	'(':  ')',
	'【':  '】',
	'[':  ']',
}

// stripSurroundingQuotes removes matching opening/closing quote or bracket
// pairs that wrap the whole string, peeling repeatedly for nested wrappers.
func stripSurroundingQuotes(s string) string {
	for {
		runes := []rune(s)
		if len(runes) < 2 {
			return s
		}
		closer, ok := chatTitleQuotePairs[runes[0]]
		if !ok || runes[len(runes)-1] != closer {
			return s
		}
		s = strings.TrimSpace(string(runes[1 : len(runes)-1]))
		if s == "" {
			return s
		}
	}
}
