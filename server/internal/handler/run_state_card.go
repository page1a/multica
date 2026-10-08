package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/statecard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Why a run opens with the state card (DENE-1331). The daemon words the
// heading from it; an empty reason is a handoff, the only one older servers
// send.
const (
	stateCardReasonHandoff      = "handoff"
	stateCardReasonBaton        = "baton"
	stateCardReasonWakeup       = "wakeup"
	stateCardReasonFreshSession = "fresh_session"
)

// The cold-and-thick gate (DENE-668 step 3, DENE-1331). Resuming a session
// whose prompt cache has expired re-pays its whole context at full price; past
// the thickness threshold that costs more than starting over from the state
// card in the same working directory.
const (
	// sessionColdAfter is the prompt-cache lifetime: past it nothing of the
	// old session is cached any more.
	sessionColdAfter = time.Hour
	// sessionThickTokens is the last step's context size above which a cold
	// resume costs more than a fresh start.
	sessionThickTokens = 100_000
)

// sessionColdAndThick reports whether a session that ended at endedAt, whose
// last step carried lastContextTokens of context, should be set aside rather
// than resumed. A missing size never trips it: only runtimes that report the
// size (Claude today) are gated.
func sessionColdAndThick(endedAt time.Time, lastContextTokens *int64, now time.Time) bool {
	if lastContextTokens == nil || endedAt.IsZero() {
		return false
	}
	return now.Sub(endedAt) > sessionColdAfter && *lastContextTokens > sessionThickTokens
}

// sessionTooColdAndThick reads the latest finished run of the session and
// applies the gate. Any read failure keeps the session.
func (h *Handler) sessionTooColdAndThick(ctx context.Context, task db.AgentTaskQueue, sessionID string) bool {
	var endedAt pgtype.Timestamptz
	var tokens pgtype.Int8
	err := h.DB.QueryRow(ctx, `
SELECT COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at),
       (SELECT MAX(u.last_context_tokens) FROM task_usage u WHERE u.task_id = t.id)
FROM agent_task_queue t
WHERE t.agent_id = $1 AND t.issue_id = $2 AND t.session_id = $3
  AND t.status IN ('completed', 'failed', 'cancelled')
ORDER BY COALESCE(t.completed_at, t.started_at, t.dispatched_at, t.created_at) DESC
LIMIT 1`, task.AgentID, task.IssueID, sessionID).Scan(&endedAt, &tokens)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("claim: read session weight failed; resuming", "task_id", uuidToString(task.ID), "error", err)
		}
		return false
	}
	if !endedAt.Valid || !tokens.Valid {
		return false
	}
	return sessionColdAndThick(endedAt.Time, &tokens.Int64, time.Now())
}

// stateCardForRun is the state card a run opens with, and why. A run gets it
// when it was just handed the issue, when someone else closed or handed off
// since this agent's previous run, when it is a wakeup, or when its old
// session was set aside. Every other run returns "" and reads the card with
// `multica issue context` when it needs it.
func (h *Handler) stateCardForRun(ctx context.Context, issue db.Issue, agent db.Agent, task db.AgentTaskQueue, wakeup, freshSession bool) (string, string) {
	meta := issueMetaStrings(issue.Metadata)
	reason := ""
	switch {
	case h.handoffCardDue(ctx, issue, meta, agent, task):
		reason = stateCardReasonHandoff
	case freshSession:
		reason = stateCardReasonFreshSession
	case wakeup:
		reason = stateCardReasonWakeup
	case h.batonSincePreviousRun(ctx, issue, meta, agent, task):
		reason = stateCardReasonBaton
	default:
		return "", ""
	}
	card, err := h.buildStateCard(ctx, issue, statecard.Caller{Type: "agent", ID: uuidToString(agent.ID)}, task.ID, nil)
	if err != nil {
		slog.Warn("claim: build state card failed", "task_id", uuidToString(task.ID), "error", err)
		return "", ""
	}
	if reason == stateCardReasonBaton && (card.Baton == nil || (card.Baton.ByType == "agent" && card.Baton.ByID == uuidToString(agent.ID))) {
		// The newest baton is this agent's own: nothing it does not know.
		return "", ""
	}
	text, _ := truncateUTF8(statecard.Render(card), maxHandoffCardBytes)
	return text, reason
}

// batonSincePreviousRun is the cheap pre-check for a baton run: the issue
// carries a close or handoff record newer than this agent's previous run on
// it (or the agent never ran here). Whose baton it is needs the full card.
func (h *Handler) batonSincePreviousRun(ctx context.Context, issue db.Issue, meta map[string]string, agent db.Agent, task db.AgentTaskQueue) bool {
	var latest time.Time
	for _, raw := range []string{meta[closeprotocol.KeyAt], meta[statecard.KeyHandoffAt]} {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil && t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return false
	}
	prev, err := h.Queries.GetAgentPreviousRunStartOnIssue(ctx, db.GetAgentPreviousRunStartOnIssueParams{
		IssueID: issue.ID, AgentID: agent.ID, ExcludeTaskID: task.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !prev.Valid) {
		return true
	}
	if err != nil {
		return false
	}
	return latest.After(prev.Time)
}
