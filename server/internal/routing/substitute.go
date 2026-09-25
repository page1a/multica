package routing

import "github.com/multica-ai/multica/server/internal/quotarelay"

// SubstituteSeat chooses who can take a wake the holder cannot.
//
// Same tier, different provider family, preferring the direction when the
// holder has one. Exactly one tier down when that tier has nobody eligible.
// IDs in avoid are never chosen — the holder, and for an acceptance wake the
// seat that did the work. The rule is the same one quota handoff already uses.
func SubstituteSeat(l Ladder, holder Seat, roster map[string]Agent, avoid []string, direction string) (Seat, bool, bool) {
	if holder.TierKey == "" {
		if key, ok := l.TierOf(holder.Name); ok {
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
		if blocked[agent.ID] {
			seat.Eligible = false
		}
		relay = append(relay, seat)
	}
	provider, _ := l.ProviderOf(holder.Name)
	failedDir := l.seatDirection(holder.Name)
	if failedDir == "" {
		failedDir = direction
	}
	failed := quotarelay.Seat{
		ID:         holder.ID,
		Name:       holder.Name,
		Tier:       holder.TierKey,
		Direction:  failedDir,
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
	tier := ""
	if key, ok := l.NormalizeTier(agent.Tier); ok && key != "" {
		tier = key
	} else if key, ok := l.TierOf(agent.Name); ok {
		tier = key
	}
	provider, _ := l.ProviderOf(agent.Name)
	return quotarelay.Seat{
		ID:        agent.ID,
		Name:      agent.Name,
		Tier:      tier,
		Direction: l.seatDirection(agent.Name),
		Provider:  provider,
		Eligible:  tier != "" && agent.ID != "",
	}
}
