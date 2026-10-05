package handler

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/statecard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Comment modes for an @agent who is not the one running (DENE-1350). The
// running agent is A, the mentioned one is B.
const (
	// commentModeHandoff stops A, writes the handoff card from the comment
	// and starts B in a new session that opens with the card.
	commentModeHandoff = "handoff"
	// commentModeParallel leaves A running and starts B alongside it.
	commentModeParallel = "parallel"
)

// maxHandoffCardBytes bounds the card a handed-to run opens with.
const maxHandoffCardBytes = 6 << 10

type freshCommentSessionKey struct{}

// withFreshCommentSession marks the comment's triggers to start new sessions:
// a handed-to agent begins from the card, not from an old conversation.
func withFreshCommentSession(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshCommentSessionKey{}, true)
}

func freshCommentSession(ctx context.Context) bool {
	v, _ := ctx.Value(freshCommentSessionKey{}).(bool)
	return v
}

// stopForCommentHandoff stops every active run on the issue whose agent the
// comment does not wake. The caller's own run is left alone: an agent that
// hands off finishes its turn by itself.
func (h *Handler) stopForCommentHandoff(r *http.Request, issue db.Issue, triggers []commentAgentTrigger, actorType, actorID string) []string {
	ctx := r.Context()
	tasks, err := h.Queries.ListActiveTasksByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("handoff comment: list active tasks failed", "issue_id", uuidToString(issue.ID), "error", err)
		return nil
	}
	woken := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		woken[uuidToString(trigger.Agent.ID)] = true
	}
	var own pgtype.UUID
	if actorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			own = task.ID
		}
	}
	var stopped []string
	for _, task := range tasks {
		if woken[uuidToString(task.AgentID)] || (own.Valid && task.ID == own) {
			continue
		}
		if _, err := h.TaskService.CancelTaskWithResult(ctx, task.ID, service.CancelTaskOptions{
			CancelledBy:   h.taskCancellationActor(ctx, actorType, actorID),
			UserInitiated: true,
		}); err != nil {
			slog.Warn("handoff comment: stop run failed", "task_id", uuidToString(task.ID), "error", err)
			continue
		}
		stopped = append(stopped, uuidToString(task.ID))
	}
	return stopped
}

var mentionLinkPattern = regexp.MustCompile(`\[(@?[^\]]*)\]\(mention://[^)]*\)`)

// handoffSummaryFromComment is the comment as one line for the card's
// 上一棒交代: mention links read as their names, clipped to the baton limit.
func handoffSummaryFromComment(content string) string {
	plain := mentionLinkPattern.ReplaceAllString(content, "$1")
	return statecard.Clip(strings.Join(strings.Fields(plain), " "), statecard.MaxBatonLen)
}

// recordCommentHandoff writes the handoff card the woken agents open with,
// through the same writer `issue handoff --summary` uses.
func (h *Handler) recordCommentHandoff(r *http.Request, issue db.Issue, content string, triggers []commentAgentTrigger) {
	names := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		names = append(names, trigger.Agent.Name)
	}
	to := strings.Join(names, "、")
	h.recordHandoff(r, issue, HandoffIssueResponse{Target: to, TargetType: "agent", TargetName: to}, handoffSummaryFromComment(content), nil)
}

// handoffCardForRun is the state card a handed-to agent's first run after the
// handoff opens with. Later runs of the same agent read it with
// `multica issue context` like everyone else.
func (h *Handler) handoffCardForRun(ctx context.Context, issue db.Issue, agent db.Agent, task db.AgentTaskQueue) string {
	meta := issueMetaStrings(issue.Metadata)
	if !handoffNamesAgent(meta[statecard.KeyHandoffTo], agent) {
		return ""
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(meta[statecard.KeyHandoffAt]))
	if err != nil || (task.CreatedAt.Valid && task.CreatedAt.Time.Before(at.Add(-time.Minute))) {
		return ""
	}
	var earlier bool
	if err := h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND id <> $3 AND created_at >= $4 AND created_at < $5)`,
		issue.ID, agent.ID, task.ID, at.Add(-time.Minute), task.CreatedAt).Scan(&earlier); err != nil || earlier {
		return ""
	}
	card, err := h.buildStateCard(ctx, issue, statecard.Caller{Type: "agent", ID: uuidToString(agent.ID)}, task.ID, nil)
	if err != nil {
		slog.Warn("claim: build handoff card failed", "task_id", uuidToString(task.ID), "error", err)
		return ""
	}
	text, _ := truncateUTF8(statecard.Render(card), maxHandoffCardBytes)
	return text
}

// handoffNamesAgent reports whether the handoff note's target list names the
// agent. A comment handoff to several agents joins their names with 、.
func handoffNamesAgent(to string, agent db.Agent) bool {
	for _, name := range strings.Split(to, "、") {
		name = strings.TrimSpace(name)
		if name != "" && (strings.EqualFold(name, agent.Name) || name == uuidToString(agent.ID)) {
			return true
		}
	}
	return false
}
