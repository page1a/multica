package routing

import (
	"context"
	"strings"
	"time"
)

// The stale-review row.
//
// Every other row of the routing table hangs off something a person did: a
// ticket was created, a status changed. This one hangs off something nobody
// did — a ticket entered "in review" and then nothing happened to it again.
// A stall is not an event, so there is no hook that can carry it, which is why
// this row has its own entry point and its own periodic caller.
//
// It is also the only row whose answer can reach a status. Two gates stand in
// front of that, and they are independent on purpose:
//
//  1. The store must report that the REVIEWER themselves said something on
//     this ticket. No remark, no completion, whatever the model answers.
//  2. The judge must read those remarks as an acceptance, at or above the
//     workspace's confidence threshold.
//
// So the model never announces that the work is finished. It can only notice
// that somebody else already did, and let the status catch up. Everything
// else — an empty thread, a request for changes, a question, an unsure
// answer, an unreachable model — falls to the default action, which is to put
// the ticket back in front of whoever accepts it and change no status at all.

// staleSweepLimit bounds how many stalled tickets one workspace contributes to
// one sweep. Each one costs a model call, and a workspace that has just turned
// routing on may have years of review backlog; taking the quietest few per
// pass drains it over a few days instead of firing hundreds of calls at once.
const staleSweepLimit = 25

// Sweep runs the stale-review row across every workspace that has routing
// switched on. It is what the periodic caller invokes; it is never reached
// from a request.
//
// A failure in one workspace does not stop the others: the sweep's job is to
// keep stalled tickets moving, and one unreadable workspace must not freeze
// the rest. Errors are logged and counted, never returned as a single fatal.
func (r *Router) Sweep(ctx context.Context) (SweepReport, error) {
	report := SweepReport{}
	workspaces, err := r.Store.EnabledWorkspaces(ctx)
	if err != nil {
		return report, err
	}
	for _, workspaceID := range workspaces {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		n, err := r.SweepWorkspace(ctx, workspaceID, &report)
		if err != nil {
			report.Failed++
			r.log().Warn("routing: stale sweep failed for workspace",
				"workspace_id", workspaceID, "error", err)
			continue
		}
		report.Examined += n
	}
	return report, nil
}

// SweepReport is what one sweep did, for the job's audit row and the log. It
// counts outcomes, not tickets: a ticket can be examined and left alone.
type SweepReport struct {
	Workspaces int
	Examined   int
	Woken      int
	Completed  int
	Failed     int
}

// SweepWorkspace runs the stale-review row for one workspace and returns how
// many tickets it examined.
func (r *Router) SweepWorkspace(ctx context.Context, workspaceID string, report *SweepReport) (int, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	// Re-checked here rather than trusted from EnabledWorkspaces: the switch
	// can be turned off between listing and sweeping, and "routing is off"
	// must mean no request and no write at every point, not only at the
	// point the list was taken.
	if !settings.State().Active() {
		return 0, nil
	}
	if open, _, _ := r.Breaker.Open(workspaceID); open {
		return 0, nil
	}
	if report != nil {
		report.Workspaces++
	}

	before := time.Now().Add(-settings.StaleAfter())
	ids, err := r.Store.StaleReviews(ctx, workspaceID, before, staleSweepLimit)
	if err != nil {
		return 0, err
	}
	examined := 0
	for _, issueID := range ids {
		if err := ctx.Err(); err != nil {
			return examined, err
		}
		out, err := r.RouteStale(ctx, workspaceID, issueID)
		examined++
		if err != nil {
			if report != nil {
				report.Failed++
			}
			r.log().Warn("routing: stale pass failed",
				"workspace_id", workspaceID, "issue_id", issueID, "error", err)
			continue
		}
		if report != nil {
			switch out.Action {
			case ActionWoken:
				report.Woken++
			case ActionCompleted:
				report.Completed++
			}
		}
	}
	return examined, nil
}

// RouteStale is the stale-review row for one ticket. It is exported so the
// same pass can be run by hand against a single issue, exactly as Route can.
//
// It repeats Route's guards rather than sharing its body, because it is a
// different question asked at a different moment — but it repeats them to the
// letter: switched off, incomplete, or cooling down all mean no request, no
// write, no comment, no mention.
func (r *Router) RouteStale(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateOff, Action: ActionSkipped, Reason: "settings unreadable"}, err
	}
	state := settings.State()
	if !state.Active() {
		return Outcome{State: state, Action: ActionSkipped, Reason: "routing not enabled"}, nil
	}
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return Outcome{State: StateIneffective, Action: ActionSkipped, Reason: reason}, nil
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return Outcome{State: state, Action: ActionSkipped, Reason: "issue unreadable"}, err
	}
	return r.routeStale(ctx, workspaceID, settings, issue)
}

func (r *Router) routeStale(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	noop := func(reason string) Outcome {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: reason}
	}

	// This row exists for exactly one status. in_progress, todo and backlog
	// are not stalls this package is entitled to have an opinion about, and a
	// ticket that left review between the listing and this pass is simply no
	// longer the ticket the sweep picked up.
	if issue.Status != "in_review" {
		return noop("status is not in_review"), nil
	}
	if issue.ParentIssueID != "" {
		return noop("sub-issue has no acceptance route"), nil
	}
	// Re-checked against the clock here as well as in SQL, so the single-issue
	// entry point cannot act on a ticket that is not actually quiet.
	quiet := issue.QuietFor(time.Now())
	if quiet < settings.StaleAfter() {
		return noop("issue is not stale yet"), nil
	}

	if issue.Reviewer.Empty() {
		// Never decided. That is the in-review row's job, not this one's, and
		// running it IS the wake: it fills the slot and hands the ticket to
		// whoever accepts it. Reusing it also means a stalled ticket and a
		// freshly-reviewed one get the same reviewer by the same rules.
		return r.routeInReview(ctx, workspaceID, settings, issue)
	}
	if issue.Reviewer.Kind == ReviewerNoReview {
		return noop("issue needs no acceptance pass"), nil
	}

	remarks, err := r.Store.ReviewRemarks(ctx, workspaceID, issue.ID, issue.Reviewer)
	if err != nil {
		return noop("review remarks unreadable"), err
	}

	decision, err := r.Judge.Stale(ctx, settings.Target(), r.staleState(issue, quiet, remarks))
	if err != nil {
		return r.reportUnavailable(ctx, workspaceID, issue, err)
	}
	r.Breaker.Succeed(workspaceID)

	// The completion gate. Both halves are required, and the first one is not
	// a model output: with nothing from the reviewer on the ticket there is
	// no acceptance for a status to be aligned TO, so no answer can unlock
	// this branch. An under-threshold answer is a weak answer to the only
	// question in this package whose mistake is invisible on the board, and
	// it falls to the default like every other weak answer here.
	if decision.Action == StaleComplete &&
		len(remarks) > 0 &&
		decision.Confidence >= settings.Threshold() {
		return r.completeStale(ctx, workspaceID, issue, decision, settings.Threshold())
	}
	return r.wakeStale(ctx, workspaceID, issue, decision, quiet)
}

// completeStale aligns the status to an acceptance the ticket already carries.
func (r *Router) completeStale(ctx context.Context, workspaceID string, issue Issue, d StaleDecision, threshold float64) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionCompleted}
	written, err := r.Store.CompleteFromReview(ctx, workspaceID, issue.ID)
	if err != nil {
		return out, err
	}
	if !written {
		// It left review between the decision and the write. Somebody else
		// moved it, and their answer is the answer.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "issue left in_review before the write"}, nil
	}
	body := r.completedComment(issue, d, threshold)
	// No mention. The transition is the good outcome, it is in the timeline,
	// and everybody on the thread already sees the comment; an @ here would
	// ping a person about a ticket that just stopped needing them.
	return r.deliver(ctx, workspaceID, issue, KindCompleted, body, false, out)
}

// wakeStale puts a stalled ticket back in front of whoever accepts it,
// changing no status and no other value.
//
// How you wake somebody depends on what they are. Assigning a seat starts its
// run, so for an agent reviewer the reassignment IS the wake, and it may
// repeat on a later sweep because a run can fail to land. A person is woken by
// a notification and by nothing else — reassigning a ticket they already hold
// wakes nobody — so that half is a comment plus an @, and the one-comment-per
// -kind index makes it happen exactly once per ticket, which is the whole
// point of the @-once rule.
func (r *Router) wakeStale(ctx context.Context, workspaceID string, issue Issue, d StaleDecision, quiet time.Duration) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionWoken}

	if issue.Reviewer.Kind == ReviewerMember {
		body := r.stalledComment(issue, d, quiet)
		target := Member{UserID: issue.Reviewer.ID, Name: issue.Reviewer.Name}
		delivered, err := r.deliverTo(ctx, workspaceID, issue, KindStalled, body, target, out)
		if err != nil {
			return delivered, err
		}
		if !delivered.Commented {
			// Already said once. Saying it again every sweep is the failure
			// mode this whole package is written against.
			return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "already told the reviewer it is stalled"}, nil
		}
		return delivered, nil
	}

	// An agent reviewer. Resolved against the roster on the spot: a seat that
	// has since been archived cannot be woken, and saying so beats handing the
	// ticket to something that cannot run.
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	seat, ok := agentByID(roster, issue.Reviewer.ID)
	if !ok {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer seat not in roster"}, nil
	}
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seat.ID); err != nil {
		return out, err
	}
	r.log().Info("routing: woke a stalled review",
		"workspace_id", workspaceID, "issue_id", issue.ID,
		"reviewer", seat.Name, "quiet_hours", int(quiet.Hours()))
	// No comment and no @: the reassignment starts the seat's run and is
	// already in the timeline, and a comment could only be posted once anyway
	// while the wake itself may need to repeat.
	return out, nil
}

// QuietFor reports how long nothing has happened on this ticket. A store that
// could not supply the timestamp yields zero, which every caller reads as
// "not stale" — the safe direction for a row that can write a status.
func (i Issue) QuietFor(now time.Time) time.Duration {
	if i.LastActivityAt.IsZero() {
		return 0
	}
	d := now.Sub(i.LastActivityAt)
	if d < 0 {
		return 0
	}
	return d
}

// staleRemarkLimit and staleRemarkRunes bound what leaves the deployment. The
// judge needs to see whether an acceptance was given, not the whole thread.
const (
	staleRemarkLimit = 6
	staleRemarkRunes = 600
)

func (r *Router) staleState(issue Issue, quiet time.Duration, remarks []string) StaleState {
	// Newest remarks, oldest first: an acceptance is the last thing said, and
	// a long review thread would otherwise be clipped at the wrong end.
	if len(remarks) > staleRemarkLimit {
		remarks = remarks[len(remarks)-staleRemarkLimit:]
	}
	trimmed := make([]string, 0, len(remarks))
	for _, m := range remarks {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		trimmed = append(trimmed, clipRunes(m, staleRemarkRunes))
	}
	return StaleState{
		JudgeState:    r.judgeState(issue, "", nil),
		QuietHours:    int(quiet.Hours()),
		Reviewer:      issue.Reviewer.Label(),
		ReviewRemarks: trimmed,
	}
}

func clipRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
