package routing

import "testing"

func TestLiveSeatsSitOnTheirTaggedRungsWithAProvider(t *testing.T) {
	l := DefaultLadder
	want := []struct {
		name     string
		tier     string
		provider string
		model    string
	}{
		{"孙悟饭", "strongest", "openai", "gpt-6-astra"},
		{"特兰克斯", "strong", "openai", "gpt-6-sol"},
		{"克林", "weak", "openai", "gpt-6-luna"},
		{"布尔玛", "strongest", "anthropic", "claude-fable-5-1"},
		{"孙悟空", "strong", "anthropic", "claude-opus-5-5"},
		{"贝吉塔", "medium", "deepseek", "command-code/deepseek%2Fdeepseek-v4.1-flash"},
		{"比克", "weak", "deepseek", "command-code2/deepseek%2Fdeepseek-v4.1-flash"},
	}
	for _, tc := range want {
		seat, tier, ok := l.SeatByName(tc.name)
		if !ok || tier != tc.tier || seat.Provider != tc.provider || seat.Model != tc.model {
			t.Errorf("%s = tier %q provider %q model %q ok=%v, want tier %q provider %q model %q",
				tc.name, tier, seat.Provider, seat.Model, ok, tc.tier, tc.provider, tc.model)
		}
		if got, ok := l.ProviderOf(tc.name + "游戏"); !ok || got != tc.provider {
			t.Errorf("%s游戏 provider = %q ok=%v, want %q", tc.name, got, ok, tc.provider)
		}
	}
}

func TestSameTierAlternatePrefersADifferentFamilyOnTheSameDirection(t *testing.T) {
	l := DefaultLadder
	holder := Seat{ID: "goku", Name: "孙悟空游戏", TierKey: "strong"}
	roster := map[string]Agent{
		"孙悟空游戏": {ID: "goku", Name: "孙悟空游戏"},
		"特兰克斯":  {ID: "trunks", Name: "特兰克斯"},
		"孙悟天游戏": {ID: "goten", Name: "孙悟天游戏"},
		"布尔玛游戏": {ID: "bulma", Name: "布尔玛游戏"},
	}
	got, ok := l.SameTierAlternate(holder, "游戏", roster)
	if !ok || got.Name != "孙悟天游戏" || got.TierKey != "strong" {
		t.Fatalf("alternate = %+v ok=%v, want 孙悟天游戏 on strong", got, ok)
	}
}

func TestEveryRoutableSeatHasAProviderFamily(t *testing.T) {
	for _, tier := range DefaultLadder.Tiers {
		if len(tier.Seats) == 0 {
			t.Fatalf("tier %s has no seats", tier.Key)
		}
		for _, seat := range tier.Seats {
			got, ok := DefaultLadder.ProviderOf(seat.Base)
			if !ok || got == "" || got != seat.Provider {
				t.Errorf("%s provider = %q ok=%v, want %q", seat.Base, got, ok, seat.Provider)
			}
		}
	}
	if _, ok := DefaultLadder.ProviderOf("不存在"); ok {
		t.Error("an unknown seat must not report a provider")
	}
}

func TestVegetaAndPiccoloAreDifferentStrengths(t *testing.T) {
	vegeta, vegetaTier, vok := DefaultLadder.SeatByName("贝吉塔")
	piccolo, piccoloTier, pok := DefaultLadder.SeatByName("比克")
	if !vok || !pok {
		t.Fatalf("missing seat: 贝吉塔 ok=%v 比克 ok=%v", vok, pok)
	}
	if vegetaTier == piccoloTier {
		t.Fatalf("贝吉塔 and 比克 share tier %q", vegetaTier)
	}
	if vegeta.Model == piccolo.Model && vegeta.Thinking == piccolo.Thinking {
		t.Fatalf("same model and thinking cannot be two rungs: %+v %+v", vegeta, piccolo)
	}
	vi, pi := tierIndex(vegetaTier), tierIndex(piccoloTier)
	if vi < 0 || pi < 0 || vi >= pi {
		t.Fatalf("贝吉塔 (%s @ %d) must sit above 比克 (%s @ %d)", vegetaTier, vi, piccoloTier, pi)
	}
}

func tierIndex(key string) int {
	for i, k := range DefaultLadder.TierKeys() {
		if k == key {
			return i
		}
	}
	return -1
}
