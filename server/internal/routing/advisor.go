package routing

import (
	"context"
	"sort"
	"strings"
)

// Advisor refusals (DENE-1721). The asking run keeps working either way; the
// reason only tells it why no advice is coming.
const (
	// AdvisorNoStrongSeat: no seat other than the asker sits on the strongest
	// rung and may be picked automatically.
	AdvisorNoStrongSeat = "no_strong_seat"
	// AdvisorAllUnavailable: there are strong seats, but every one is out of
	// quota, offline or switched off right now.
	AdvisorAllUnavailable = "all_unavailable"
)

// AdvisorCandidates answers "who on the strongest rung could answer a
// question about this ticket", best first. The asker is never on the list.
// Order: a seat that fits the ticket's direction before a generic one, an
// idle seat before a busy one, then usage headroom, then name. An empty list
// comes with the refusal saying why.
//
// The caller still checks what routing cannot see — whether the seat's
// daemon can run a consult and whether it has a free slot — and takes the
// first candidate that passes.
func (r *Router) AdvisorCandidates(ctx context.Context, workspaceID, issueID, askerID string) ([]Seat, string, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return nil, "", err
	}
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	ladder := r.Ladder.For(settings)
	roster = ForProject(roster, issue.ProjectID)
	scene := ladder.IssueScene(issue)
	strong := strongestSeats(ladder, roster, askerID)
	if len(strong) == 0 {
		return nil, AdvisorNoStrongSeat, nil
	}
	ids := make([]string, 0, len(strong))
	for _, a := range strong {
		ids = append(ids, a.ID)
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, ids, settings.ProviderKeys())
	if err != nil {
		return nil, "", err
	}
	seats := RankAdvisors(ladder, scene, strong, facts)
	if len(seats) == 0 {
		return nil, AdvisorAllUnavailable, nil
	}
	return seats, "", nil
}

// strongestSeats is every seat on the ladder's top rung that automatic
// dispatch may pick for this ticket, minus the asker.
func strongestSeats(l Ladder, roster map[string]Agent, askerID string) []Agent {
	if len(l.Tiers) == 0 {
		return nil
	}
	top := l.Tiers[0].Key
	out := make([]Agent, 0, 4)
	for _, a := range roster {
		if a.ID == "" || a.ID == askerID {
			continue
		}
		tier := agentTierKey(l, a)
		if !strings.EqualFold(tier, top) {
			continue
		}
		if ok, _ := SeatSelectable(a.State, SelectContext{NeedTier: true, Tier: tier, ProjectID: a.project}); !ok {
			continue
		}
		out = append(out, a)
	}
	return out
}

// RankAdvisors drops seats the snapshot rules out and orders the rest.
func RankAdvisors(l Ladder, scene Scene, seats []Agent, facts RoutingFacts) []Seat {
	type ranked struct {
		agent   Agent
		fit     int
		running int
		usage   int
	}
	list := make([]ranked, 0, len(seats))
	for _, a := range seats {
		if snap, ok := facts.Seats[a.ID]; ok && Unselectable(snap.Availability) {
			continue
		}
		list = append(list, ranked{agent: a, fit: l.AgentFit(scene, a).Rank(), running: facts.Running[a.ID], usage: l.usageRank(a)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.fit != b.fit {
			return a.fit < b.fit
		}
		if (a.running == 0) != (b.running == 0) {
			return a.running == 0
		}
		if a.usage != b.usage {
			return a.usage < b.usage
		}
		if a.agent.Name != b.agent.Name {
			return a.agent.Name < b.agent.Name
		}
		return a.agent.ID < b.agent.ID
	})
	out := make([]Seat, 0, len(list))
	for _, item := range list {
		seat := Seat{ID: item.agent.ID, Name: item.agent.Name, TierKey: agentTierKey(l, item.agent), Direction: l.agentDirection(item.agent)}
		if tier, ok := l.TierByKey(seat.TierKey); ok {
			seat.TierLabel = tier.Label
		}
		out = append(out, seat)
	}
	return out
}
