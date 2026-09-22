package routing

import (
	"context"
	"sync"
)

// SuggestRow is one issue that does not exist yet: a row of a draft the user
// is about to confirm.
type SuggestRow struct {
	Title              string
	DescriptionSummary string
	HasChildren        bool
}

// Suggestion is the seat routing would put in a row's executor slot. A nil
// Seat means routing has no answer for that row and the slot stays empty;
// Reason says why, for logs.
type Suggestion struct {
	Seat   *Seat
	Reason string
}

// Suggest answers the todo row's executor question for issues that have not
// been created yet, so the draft confirm panel can show the seat BEFORE the
// user confirms instead of a ticket being dispatched out of sight afterwards
// (DENE-691).
//
// It is the same ladder, the same direction table and the same judge as
// routeTodo, and it writes nothing: no assignee, no comment, no mention. A row
// the judge cannot place confidently comes back empty rather than on the
// fallback rung — routeTodo falls back because an unheld ticket is invisible,
// while a draft row is on screen in front of the person who will pick.
func (r *Router) Suggest(ctx context.Context, workspaceID, projectName string, rows []SuggestRow) ([]Suggestion, error) {
	out := make([]Suggestion, len(rows))
	fill := func(reason string) []Suggestion {
		for i := range out {
			out[i].Reason = reason
		}
		return out
	}

	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return fill("settings unreadable"), err
	}
	if !settings.State().Active() {
		return fill("routing not enabled"), nil
	}
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return fill(reason), nil
	}
	if a, ok := r.Judge.(Availability); ok && !a.Available(settings.Target()) {
		return fill(NotConfiguredReason), nil
	}

	ladder := r.Ladder.WithProjects(settings.Projects)
	direction := ladder.ResolveDirection(projectName).Direction
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return fill("roster unreadable"), err
	}
	candidates := ladder.Candidates(direction, roster)
	if len(candidates) == 0 {
		return fill("ladder has no seat in this workspace"), nil
	}
	threshold := settings.Threshold()

	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func(i int, row SuggestRow) {
			defer wg.Done()
			state := r.judgeState(Issue{
				Title:              row.Title,
				DescriptionSummary: row.DescriptionSummary,
				Status:             "todo",
				ProjectName:        projectName,
				HasChildren:        row.HasChildren,
			}, direction, candidates)
			v, err := r.Judge.Assign(ctx, settings.Target(), state)
			if err != nil {
				r.Breaker.Fail(workspaceID, err)
				out[i].Reason = "judge unavailable"
				return
			}
			r.Breaker.Succeed(workspaceID)
			seat, ok := SeatByTier(candidates, v.ExecutorTier)
			switch {
			case !ok:
				out[i].Reason = "judge named tier \"" + v.ExecutorTier + "\", which has no seat here"
			case v.ExecutorConfidence < threshold:
				out[i].Reason = "confidence " + pct(v.ExecutorConfidence) + " < threshold " + pct(threshold)
			default:
				out[i].Seat = &seat
			}
		}(i, row)
	}
	wg.Wait()
	return out, nil
}
