package routing

import "testing"

func TestStrongestSeatsSkipsTheAskerAndWeakerTiers(t *testing.T) {
	roster := map[string]Agent{
		"孙悟饭": {ID: "gohan", Name: "孙悟饭"},
		"布尔玛": {ID: "bulma", Name: "布尔玛"},
		"孙悟空": {ID: "goku", Name: "孙悟空"},
	}
	got := strongestSeats(DefaultLadder, roster, "bulma")
	if len(got) != 1 || got[0].ID != "gohan" {
		t.Fatalf("strongest = %+v, want only 孙悟饭 (asker and weaker tiers left out)", got)
	}
	if got := strongestSeats(DefaultLadder, map[string]Agent{"孙悟空": {ID: "goku", Name: "孙悟空"}}, "x"); len(got) != 0 {
		t.Fatalf("strongest = %+v, want none when nobody sits on the top rung", got)
	}
}

func TestRankAdvisorsDropsUnavailableAndPrefersIdle(t *testing.T) {
	seats := []Agent{{ID: "bulma", Name: "布尔玛"}, {ID: "gohan", Name: "孙悟饭"}}
	facts := RoutingFacts{Seats: map[string]SeatSnapshot{}, Running: map[string]int{"bulma": 2}}
	got := RankAdvisors(DefaultLadder, GenericScene, seats, facts)
	if len(got) != 2 || got[0].ID != "gohan" {
		t.Fatalf("ranked = %+v, want the idle 孙悟饭 first", got)
	}
	facts.Seats["gohan"] = SeatSnapshot{AgentID: "gohan", Availability: AvailabilityQuotaExhausted}
	got = RankAdvisors(DefaultLadder, GenericScene, seats, facts)
	if len(got) != 1 || got[0].ID != "bulma" {
		t.Fatalf("ranked = %+v, want the exhausted seat dropped", got)
	}
	facts.Seats["bulma"] = SeatSnapshot{AgentID: "bulma", Availability: AvailabilityDead}
	if got = RankAdvisors(DefaultLadder, GenericScene, seats, facts); len(got) != 0 {
		t.Fatalf("ranked = %+v, want nobody when every strong seat is out", got)
	}
}
