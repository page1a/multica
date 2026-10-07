package routing

import (
	"context"
	"errors"
	"strings"
)

// ErrNotEscalatable is returned when a ticket has no agent executor to move
// up from, or is already over.
var ErrNotEscalatable = errors.New("routing: ticket has no agent executor to escalate from")

// Escalation is the answer to "the executor says this is too hard".
type Escalation struct {
	// Changed is true when the executor slot now holds a different seat.
	Changed bool
	From    string
	To      string
	// AtTop is true when the holder already sits on the strongest rung, so
	// there is nowhere to move it. Nothing is written and nobody is pinged:
	// the reason stays on the ticket for whoever reads it.
	AtTop bool
	// Commented reports that the routing comment for this move was posted.
	Commented bool
}

// Escalate is the only thing an executor may say about who should work on its
// ticket mid-flight (DENE-1033): "this is too hard", with a reason. It takes
// no person and no tier, so it cannot be used to pick a seat. Routing
// re-judges from scratch with the reason in front of it and moves the ticket
// up only if the answer is a stronger seat; a judge that does not say so, or
// is unavailable, still moves it one rung, because the request is exactly
// that.
func (r *Router) Escalate(ctx context.Context, workspaceID, issueID, reason string) (Escalation, error) {
	var esc Escalation
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		if err == nil {
			err = ErrNotEnabled
		}
		return esc, err
	}
	switch issue.Status {
	case "done", "cancelled":
		return esc, ErrNotEscalatable
	}
	if issue.AssigneeType != "agent" || issue.AssigneeID == "" {
		return esc, ErrNotEscalatable
	}

	ladder := r.Ladder.For(settings)
	scene := ladder.IssueScene(issue)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return esc, err
	}
	var holder Agent
	for _, a := range roster {
		if a.ID == issue.AssigneeID {
			holder = a
			break
		}
	}
	if holder.ID == "" {
		return esc, ErrNotEscalatable
	}
	esc.From = holder.Name
	// The tag a person put on the seat is its rung; ladder.json's name table
	// is only the fallback for an untagged seat. Reading the name first sent
	// a seat tagged 中 but listed as 强 straight to the strongest rung.
	holderTier := agentTierKey(ladder, holder)
	holderRank := tierRank(ladder, holderTier)
	if holderRank < 0 {
		return esc, ErrNotEscalatable
	}

	candidates := ladder.SceneCandidates(scene, roster)
	eligible, state, err := r.decisionContext(ctx, workspaceID, settings, issue, scene, candidates)
	if err != nil {
		return esc, err
	}
	state.EscalationReason = strings.TrimSpace(reason)
	stronger := make([]Seat, 0, len(eligible))
	for _, s := range eligible {
		if rank := tierRank(ladder, s.TierKey); rank >= 0 && rank < holderRank {
			stronger = append(stronger, s)
		}
	}
	if len(stronger) == 0 {
		esc.AtTop = true
		return esc, nil
	}

	var seat Seat
	picked := false
	if d, derr := r.decide(ctx, workspaceID, settings, issue, state); derr == nil {
		if s, found := SeatByTier(stronger, d.Verdict.ExecutorTier); found && d.Verdict.ExecutorConfidence >= settings.Threshold() {
			seat, picked = s, true
		}
	}
	if !picked {
		// One rung up: the strongest rung among the candidates that is still
		// weaker than every other stronger one, i.e. the closest above.
		seat = stronger[0]
		for _, s := range stronger[1:] {
			if tierRank(ladder, s.TierKey) > tierRank(ladder, seat.TierKey) {
				seat = s
			}
		}
	}

	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seat.ID); err != nil {
		return esc, err
	}
	esc.Changed, esc.To = true, seat.Name

	body := escalationComment(holder.Name, seat.Name, reason)
	out, err := r.deliver(ctx, workspaceID, issue, CommentKind("escalation:"+issue.AssigneeID+":"+seat.ID), body, false, Outcome{})
	esc.Commented = out.Commented
	return esc, err
}

// tierRank is a rung's position in the ladder, 0 being the strongest; -1 for a
// key the ladder does not know.
func tierRank(l Ladder, key string) int {
	for i, t := range l.Tiers {
		if strings.EqualFold(t.Key, key) {
			return i
		}
	}
	return -1
}

func escalationComment(from, to, reason string) string {
	var b strings.Builder
	b.WriteString("**路由：执行人反馈「太难」，已改派**\n\n")
	b.WriteString("原执行人 " + from + " 反馈这张票超出它的能力，路由重新判断后改派给 " + to + "。")
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString("\n\n> 原因：" + reason)
	}
	return b.String()
}
