package routing

import "testing"

// ADR-0008: a tier says how strong a seat is; dispatch_mode says whether
// automatic dispatch may pick it. These tests pin mention_only out of every
// automatic path that lives in this package. Deleting the mention_only case
// in SeatSelectable must turn them red.

var mentionOnly = SeatState{DispatchMode: DispatchMentionOnly}

func TestSeatSelectableMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  SeatState
		ctx    SelectContext
		ok     bool
		reason string
	}{
		{"zero value is selectable", SeatState{}, SelectContext{}, true, ""},
		{"explicit auto", SeatState{DispatchMode: DispatchAuto}, SelectContext{}, true, ""},
		{"mention_only", mentionOnly, SelectContext{}, false, ReasonMentionOnly},
		{"mention_only with a tier", mentionOnly, SelectContext{NeedTier: true, Tier: "weak"}, false, ReasonMentionOnly},
		{"archived first", SeatState{Archived: true, DispatchMode: DispatchMentionOnly}, SelectContext{}, false, ReasonArchived},
		{"disabled", SeatState{Disabled: true}, SelectContext{}, false, ReasonDisabled},
		{"no runtime", SeatState{NoRuntime: true}, SelectContext{}, false, ReasonNoRuntime},
		{"untiered when the path ranks by tier", SeatState{}, SelectContext{NeedTier: true}, false, ReasonUntiered},
		{"untiered is fine for initial dispatch", SeatState{}, SelectContext{}, true, ""},
		{"breaker open", SeatState{}, SelectContext{BreakerOpen: true}, false, ReasonBreakerOpen},
		{"quota exhausted", SeatState{}, SelectContext{Availability: AvailabilityQuotaExhausted}, false, AvailabilityQuotaExhausted},
		{"unknown availability keeps the seat", SeatState{}, SelectContext{Availability: AvailabilityUnknown}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := SeatSelectable(tc.state, tc.ctx)
			if ok != tc.ok || reason != tc.reason {
				t.Fatalf("SeatSelectable = (%v, %q), want (%v, %q)", ok, reason, tc.ok, tc.reason)
			}
		})
	}
}

func TestNormalizeDispatchMode(t *testing.T) {
	for in, want := range map[string]string{"": DispatchAuto, "AUTO": DispatchAuto, " mention_only ": DispatchMentionOnly} {
		if got, ok := NormalizeDispatchMode(in); !ok || got != want {
			t.Errorf("NormalizeDispatchMode(%q) = (%q, %v), want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeDispatchMode("manual"); ok {
		t.Error("an unknown mode must be refused")
	}
}

// Initial dispatch: a tagged mention_only seat is not the rung's candidate,
// and the judge's list does not contain it.
func TestSceneCandidatesDropMentionOnlyTaggedSeat(t *testing.T) {
	roster := map[string]Agent{
		"孙悟空": {ID: "goku", Name: "孙悟空", Tier: "strong"},
		"拉蒂兹": {ID: "raditz", Name: "拉蒂兹", Tier: "weak", State: mentionOnly},
	}
	for _, seat := range DefaultLadder.SceneCandidates(GenericScene, roster) {
		if seat.ID == "raditz" {
			t.Fatalf("mention_only seat reached the candidate list: %+v", seat)
		}
	}

	roster["克林"] = Agent{ID: "krillin", Name: "克林", Tier: "weak"}
	weak, ok := SeatByTier(DefaultLadder.SceneCandidates(GenericScene, roster), "weak")
	if !ok || weak.ID != "krillin" {
		t.Fatalf("weak rung = %+v ok=%v, want the auto seat 克林", weak, ok)
	}
}

// The untagged name-convention fallback must not drag a mention_only seat
// back onto its rung either.
func TestSceneCandidatesDropMentionOnlyNamedSeat(t *testing.T) {
	roster := map[string]Agent{
		"比克": {ID: "piccolo", Name: "比克", State: mentionOnly},
	}
	if _, ok := SeatByTier(DefaultLadder.SceneCandidates(GenericScene, roster), "weak"); ok {
		t.Fatal("untagged mention_only base seat was placed by its name")
	}
}

// Seat relay and the routing-side substitute: a mention_only seat on the
// right rung and family is passed over.
func TestSubstituteSeatSkipsMentionOnly(t *testing.T) {
	roster := map[string]Agent{
		"孙悟饭": {ID: "gohan", Name: "孙悟饭"},
		"布尔玛": {ID: "bulma", Name: "布尔玛", State: mentionOnly},
	}
	got, _, ok := SubstituteSeat(DefaultLadder, Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, roster, nil, GenericScene)
	if ok && got.ID == "bulma" {
		t.Fatalf("substitute picked the mention_only seat: %+v", got)
	}
}

func TestSceneTierAlternateSkipsMentionOnly(t *testing.T) {
	roster := map[string]Agent{
		"孙悟饭": {ID: "gohan", Name: "孙悟饭"},
		"布尔玛": {ID: "bulma", Name: "布尔玛", State: mentionOnly},
	}
	if got, ok := DefaultLadder.SceneTierAlternate(Seat{ID: "gohan", Name: "孙悟饭", TierKey: "strongest"}, GenericScene, roster); ok {
		t.Fatalf("same-tier alternate picked the mention_only seat: %+v", got)
	}
}

// 接着做 does not hand a ticket to a mention_only seat just because it ran
// the related one.
func TestContinuationSkipsMentionOnly(t *testing.T) {
	seat := ContinuationSeat{
		Seat:         Seat{ID: "raditz", Name: "拉蒂兹", TierKey: "weak"},
		OnRoster:     true,
		Unpickable:   ReasonMentionOnly,
		Availability: AvailabilityAvailable,
	}
	if why := continuationBlocker(seat, true, map[string]int{"weak": 3}, 3, true, GenericScene); why == "" {
		t.Fatal("a mention_only seat continued a related ticket automatically")
	}
}
