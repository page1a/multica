package routing

import "github.com/multica-ai/multica/server/internal/quotarelay"

// SubstituteSeat chooses who can take a wake the holder cannot.
//
// Same tier, different provider family, preferring the seat that fits the
// issue's scene (AgentFit). Exactly one tier down when that tier has nobody eligible.
// IDs in avoid are never chosen — the holder, and for an acceptance wake the
// seat that did the work. The rule is the same one quota handoff already uses.
func SubstituteSeat(l Ladder, holder Seat, roster map[string]Agent, avoid []string, scene Scene) (Seat, bool, bool) {
	holderAgent, onRoster := agentByID(roster, holder.ID)
	if holder.TierKey == "" {
		if onRoster {
			holder.TierKey = agentTierKey(l, holderAgent)
		} else if key, ok := l.TierOf(holder.Name); ok {
			holder.TierKey = key
		}
	}
	if holder.TierKey == "" {
		return Seat{}, false, false
	}
	blocked := map[string]bool{holder.ID: true}
	for _, id := range avoid {
		if id != "" {
			blocked[id] = true
		}
	}
	relay := make([]quotarelay.Seat, 0, len(roster))
	for _, agent := range roster {
		seat := relaySeat(l, agent)
		seat.Fit = l.AgentFit(scene, agent).Rank()
		if blocked[agent.ID] {
			seat.Eligible = false
		}
		relay = append(relay, seat)
	}
	provider, _ := l.ProviderFor(holder.Name, holderAgent.Model)
	failed := quotarelay.Seat{
		ID:         holder.ID,
		Name:       holder.Name,
		Tier:       holder.TierKey,
		Provider:   provider,
		Eligible:   false,
		AvoidHouse: provider,
	}
	choice, ok := quotarelay.Pick(failed, relay, l.TierKeys())
	if !ok {
		return Seat{}, false, false
	}
	out := Seat{
		ID:        choice.Seat.ID,
		Name:      choice.Seat.Name,
		TierKey:   choice.Seat.Tier,
		Direction: choice.Seat.Direction,
	}
	if tier, found := l.TierByKey(out.TierKey); found {
		out.TierLabel = tier.Label
	}
	return out, choice.SteppedDown, true
}

func relaySeat(l Ladder, agent Agent) quotarelay.Seat {
	tier := agentTierKey(l, agent)
	provider, _ := l.ProviderFor(agent.Name, agent.Model)
	return quotarelay.Seat{
		ID:        agent.ID,
		Name:      agent.Name,
		Tier:      tier,
		Direction: l.agentDirection(agent),
		Provider:  provider,
		Eligible:  agent.ID != "" && relayPickable(agent, tier),
		UsageRank: l.usageRank(agent),
		Demoted:   agent.Demoted,
	}
}

func relayPickable(agent Agent, tier string) bool {
	ok, _ := SeatSelectable(agent.State, SelectContext{NeedTier: true, Tier: tier, ProjectID: agent.project})
	return ok
}
