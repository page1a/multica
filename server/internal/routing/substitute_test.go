package routing

import "testing"

func TestSubstituteSeatPrefersAnotherFamilyOnTheSameTier(t *testing.T) {
	roster := map[string]Agent{
		"孙悟饭": {ID: "gohan", Name: "孙悟饭"},
		"布尔玛": {ID: "bulma", Name: "布尔玛"},
		"孙悟空": {ID: "goku", Name: "孙悟空"},
	}
	got, down, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, nil, "")
	if !ok || down || got.Name != "布尔玛" {
		t.Fatalf("substitute = %+v down=%v ok=%v, want 布尔玛 on the same tier", got, down, ok)
	}
}

func TestSubstituteSeatSkipsTheExecutorAndStepsDown(t *testing.T) {
	roster := map[string]Agent{
		"布尔玛": {ID: "bulma", Name: "布尔玛"},
		"孙悟空": {ID: "goku", Name: "孙悟空"},
		"孙悟天": {ID: "goten", Name: "孙悟天"},
	}
	got, down, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, []string{"bulma"}, "")
	if !ok || !down {
		t.Fatalf("substitute = %+v down=%v ok=%v, want one tier down", got, down, ok)
	}
	if got.TierKey != "strong" || got.ID == "bulma" {
		t.Fatalf("substitute = %+v, want a strong-tier seat that is not the executor", got)
	}
}

func TestSubstituteSeatRefusesWhenNobodyElseCanTakeIt(t *testing.T) {
	roster := map[string]Agent{
		"布尔玛": {ID: "bulma", Name: "布尔玛"},
	}
	_, _, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, []string{"bulma"}, "")
	if ok {
		t.Fatal("the only other seat is the executor and nothing sits one tier down")
	}
}
