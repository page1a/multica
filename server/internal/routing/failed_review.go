package routing

import "context"

// CoverFailedReview is the patrol's answer to an in-review ticket whose last
// acceptance run failed and nothing has run since (DENE-1647). The in-review
// row will not act on it: a failed run still counts as this stay's wake, so
// Route answers "already handed off". This entry point decides again:
//
//   - the reviewer seat is switched off, or force is set because its runs
//     keep failing: hand acceptance to another seat that did not work on the
//     ticket, the same substitute the in-review row uses;
//   - otherwise the failure may have been passing: wake the same seat once.
//
// ActionAdvised means nobody can cover; the caller asks a person. A ticket
// whose reviewer is a person, or which needs no acceptance, is not this
// row's to touch.
func (r *Router) CoverFailedReview(ctx context.Context, workspaceID, issueID string, force bool) (Outcome, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateOff, Action: ActionSkipped, Reason: "settings unreadable"}, err
	}
	state := settings.State()
	if !state.Active() {
		return Outcome{State: state, Action: ActionSkipped, Reason: "routing not enabled"}, nil
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return Outcome{State: state, Action: ActionSkipped, Reason: "issue unreadable"}, err
	}
	noop := func(reason string) Outcome {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: reason}
	}
	if issue.Status != "in_review" {
		return noop("status is not in_review"), nil
	}
	if issue.Reviewer.Kind != ReviewerAgent || issue.Reviewer.ID == "" {
		return noop("reviewer is not a seat"), nil
	}
	acceptance, err := r.Store.Acceptance(ctx, workspaceID, issue)
	if err != nil {
		return noop("acceptance state unreadable"), err
	}
	if acceptance.ActiveRun {
		return noop("active run in progress"), nil
	}
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return Outcome{}, err
	}
	roster = ForProject(roster, issue.ProjectID)
	out := Outcome{State: StateEnabled, Action: ActionHandedOff}
	seat, onRoster := agentByID(roster, issue.Reviewer.ID)
	if !onRoster {
		card, found, err := r.Store.OffRosterSeat(ctx, workspaceID, issue.Reviewer.ID)
		if err != nil {
			return out, err
		}
		if !found {
			return noop("reviewer seat not in roster"), nil
		}
		return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, card, out, true)
	}
	if !force {
		facts, err := r.Store.RoutingFacts(ctx, workspaceID, []string{seat.ID}, settings.ProviderKeys())
		if err != nil {
			return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
		}
		snap, ok := facts.Seats[seat.ID]
		if !ok || !Unselectable(snap.Availability) {
			if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seat.ID); err != nil {
				return out, err
			}
			return Outcome{State: StateEnabled, Action: ActionWoken, Reason: "retried the reviewer seat after a failed run"}, nil
		}
	}
	return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, seat, out, true)
}
