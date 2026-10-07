package routing

import (
	"context"
	"strings"
	"testing"
)

// Usage orders the seats on one rung (DENE-922): out of quota is skipped,
// then ample goes first, then the name decides. 「允许上调一档」 lets a rung
// whose every seat is tight borrow an ample seat from the rung above.

func TestNormalizeUsageAcceptsKeyAndLabel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"tight", UsageTight, true},
		{"AMPLE", UsageAmple, true},
		{"常规", UsageNormal, true},
		{" 充足 ", UsageAmple, true},
		{"紧张", UsageTight, true},
		{"", "", false},
		{"plenty", "", false},
	} {
		got, ok := NormalizeUsage(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeUsage(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSeatOrderDefaultsToUsageOnUpshiftOff(t *testing.T) {
	// A workspace saved before the switches existed has neither key.
	s := ParseSettings([]byte(`{"routing":{"enabled":true,"model":"m"}}`))
	if got := s.SeatOrder(); got.IgnoreUsage || got.AllowUpshift {
		t.Errorf("default order = %+v, want usage on and upshift off", got)
	}
	s = ParseSettings([]byte(`{"routing":{"usage_priority":false,"allow_upshift":true}}`))
	if got := s.SeatOrder(); !got.IgnoreUsage || !got.AllowUpshift {
		t.Errorf("explicit order = %+v, want usage off and upshift on", got)
	}
}

func mediumPick(t *testing.T, l Ladder, roster map[string]Agent) Seat {
	t.Helper()
	seat, ok := SeatByTier(l.Candidates("", roster), "medium")
	if !ok {
		t.Fatalf("no medium seat in %+v", l.Candidates("", roster))
	}
	return seat
}

func TestAmpleSeatGoesFirstOnItsRung(t *testing.T) {
	roster := map[string]Agent{
		"A": {ID: "a", Name: "A", Tier: "medium", Usage: UsageTight},
		"B": {ID: "b", Name: "B", Tier: "medium", Usage: UsageNormal},
		"C": {ID: "c", Name: "C", Tier: "medium", Usage: UsageAmple},
	}
	if got := mediumPick(t, DefaultLadder, roster); got.Name != "C" {
		t.Errorf("picked %s, want the ample seat C", got.Name)
	}
	// Same usage: the name decides, as before.
	roster["C"] = Agent{ID: "c", Name: "C", Tier: "medium", Usage: UsageNormal}
	if got := mediumPick(t, DefaultLadder, roster); got.Name != "B" {
		t.Errorf("picked %s, want B — first by name among the normal seats", got.Name)
	}
	// Untagged usage reads as normal, not as tight.
	roster["C"] = Agent{ID: "c", Name: "C", Tier: "medium"}
	if got := mediumPick(t, DefaultLadder, roster); got.Name != "B" {
		t.Errorf("picked %s, want B", got.Name)
	}
}

func TestUsageOffFallsBackToName(t *testing.T) {
	roster := map[string]Agent{
		"A": {ID: "a", Name: "A", Tier: "medium", Usage: UsageTight},
		"C": {ID: "c", Name: "C", Tier: "medium", Usage: UsageAmple},
	}
	l := DefaultLadder.WithSeatOrder(SeatOrder{IgnoreUsage: true})
	if got := mediumPick(t, l, roster); got.Name != "A" {
		t.Errorf("picked %s, want A — usage is switched off", got.Name)
	}
}

func TestOutOfQuotaSkippedBeforeUsage(t *testing.T) {
	roster := map[string]Agent{
		"A": {ID: "a", Name: "A", Tier: "medium", Usage: UsageTight},
		"C": {ID: "c", Name: "C", Tier: "medium", Usage: UsageAmple, Demoted: true},
	}
	if got := mediumPick(t, DefaultLadder, roster); got.Name != "A" {
		t.Errorf("picked %s, want A — C is out of quota however ample it is tagged", got.Name)
	}
}

func upshiftRoster() map[string]Agent {
	return map[string]Agent{
		"M1": {ID: "m1", Name: "M1", Tier: "medium", Usage: UsageTight},
		"M2": {ID: "m2", Name: "M2", Tier: "medium", Usage: UsageTight},
		"S1": {ID: "s1", Name: "S1", Tier: "strong", Usage: UsageNormal},
		"S2": {ID: "s2", Name: "S2", Tier: "strong", Usage: UsageAmple},
	}
}

func TestUpshiftIsOffByDefault(t *testing.T) {
	got := mediumPick(t, DefaultLadder, upshiftRoster())
	if got.Name != "M1" || got.Upshifted {
		t.Errorf("picked %+v, want M1 on its own rung", got)
	}
}

func TestUpshiftTakesTheAmpleSeatAbove(t *testing.T) {
	l := DefaultLadder.WithSeatOrder(SeatOrder{AllowUpshift: true})
	got := mediumPick(t, l, upshiftRoster())
	if got.Name != "S2" || !got.Upshifted || got.TierKey != "medium" {
		t.Errorf("picked %+v, want S2 serving the medium rung, marked upshifted", got)
	}
	// The strong rung itself is untouched.
	strong, _ := SeatByTier(l.Candidates("", upshiftRoster()), "strong")
	if strong.Name != "S2" || strong.Upshifted {
		t.Errorf("strong rung = %+v, want S2 as its own pick", strong)
	}
}

func TestUpshiftNeedsEveryRungSeatTight(t *testing.T) {
	l := DefaultLadder.WithSeatOrder(SeatOrder{AllowUpshift: true})
	roster := upshiftRoster()
	roster["M2"] = Agent{ID: "m2", Name: "M2", Tier: "medium", Usage: UsageNormal}
	if got := mediumPick(t, l, roster); got.Name != "M2" || got.Upshifted {
		t.Errorf("picked %+v, want M2 — the rung still has a seat that is not tight", got)
	}
}

func TestUpshiftIgnoresOutOfQuotaSeatsOnTheRung(t *testing.T) {
	l := DefaultLadder.WithSeatOrder(SeatOrder{AllowUpshift: true})
	roster := upshiftRoster()
	// The only non-tight medium seat is out of quota, so the rung is tight.
	roster["M3"] = Agent{ID: "m3", Name: "M3", Tier: "medium", Usage: UsageAmple, Demoted: true}
	if got := mediumPick(t, l, roster); got.Name != "S2" || !got.Upshifted {
		t.Errorf("picked %+v, want S2 upshifted", got)
	}
}

func TestUpshiftNeedsAnAmpleSeatAbove(t *testing.T) {
	l := DefaultLadder.WithSeatOrder(SeatOrder{AllowUpshift: true})
	roster := upshiftRoster()
	roster["S2"] = Agent{ID: "s2", Name: "S2", Tier: "strong", Usage: UsageNormal}
	if got := mediumPick(t, l, roster); got.Name != "M1" || got.Upshifted {
		t.Errorf("picked %+v, want M1 — nothing ample above to borrow", got)
	}
	// An ample seat above that is out of quota does not count either.
	roster["S2"] = Agent{ID: "s2", Name: "S2", Tier: "strong", Usage: UsageAmple, Demoted: true}
	if got := mediumPick(t, l, roster); got.Name != "M1" || got.Upshifted {
		t.Errorf("picked %+v, want M1 — the ample seat above is out of quota", got)
	}
}

func TestUpshiftNeedsUsageOn(t *testing.T) {
	l := DefaultLadder.WithSeatOrder(SeatOrder{AllowUpshift: true, IgnoreUsage: true})
	if got := mediumPick(t, l, upshiftRoster()); got.Upshifted {
		t.Errorf("picked %+v, want no upshift while usage is switched off", got)
	}
}

func TestSameTierAlternatePrefersAmple(t *testing.T) {
	// 孙悟空 is anthropic; both others are another family on the strong rung.
	// By name 孙悟天 comes first; by usage 特兰克斯 does.
	roster := map[string]Agent{
		"孙悟空":  {ID: "goku", Name: "孙悟空", Tier: "strong"},
		"孙悟天":  {ID: "goten", Name: "孙悟天", Tier: "strong", Usage: UsageTight},
		"特兰克斯": {ID: "trunks", Name: "特兰克斯", Tier: "strong", Usage: UsageAmple},
	}
	holder := Seat{ID: "goku", Name: "孙悟空", TierKey: "strong"}
	got, ok := DefaultLadder.SameTierAlternate(holder, "", roster)
	if !ok || got.Name != "特兰克斯" {
		t.Errorf("acceptance seat = %+v, want 特兰克斯", got)
	}
	got, _ = DefaultLadder.WithSeatOrder(SeatOrder{IgnoreUsage: true}).SameTierAlternate(holder, "", roster)
	if got.Name != "孙悟天" {
		t.Errorf("with usage off = %+v, want 孙悟天 by name", got)
	}
}

func TestSubstitutePrefersAmple(t *testing.T) {
	roster := map[string]Agent{
		"孙悟空":  {ID: "goku", Name: "孙悟空", Tier: "strong"},
		"孙悟天":  {ID: "goten", Name: "孙悟天", Tier: "strong", Usage: UsageTight},
		"特兰克斯": {ID: "trunks", Name: "特兰克斯", Tier: "strong", Usage: UsageAmple},
	}
	holder := Seat{ID: "goku", Name: "孙悟空", TierKey: "strong"}
	got, down, ok := SubstituteSeat(DefaultLadder, holder, roster, nil, GenericScene)
	if !ok || down || got.Name != "特兰克斯" {
		t.Errorf("substitute = %+v (down=%v ok=%v), want 特兰克斯", got, down, ok)
	}
	got, _, _ = SubstituteSeat(DefaultLadder.WithSeatOrder(SeatOrder{IgnoreUsage: true}), holder, roster, nil, GenericScene)
	if got.Name != "孙悟天" {
		t.Errorf("with usage off = %+v, want 孙悟天 by name", got)
	}
}

func TestRouteUpshiftsAndSaysSo(t *testing.T) {
	store := newFakeStore()
	store.settings.AllowUpshift = true
	store.issue.ProjectName = ""
	store.roster = map[string]Agent{
		"贝吉塔":    {ID: "a-vegeta", Name: "贝吉塔", Tier: "medium", Usage: UsageTight},
		"人造人18号": {ID: "a-18", Name: "人造人18号", Tier: "medium", Usage: UsageTight},
		"孙悟空":    {ID: "a-goku", Name: "孙悟空", Tier: "strong", Usage: UsageNormal},
		"特兰克斯":   {ID: "a-trunks", Name: "特兰克斯", Tier: "strong", Usage: UsageAmple},
		"比克":     {ID: "a-piccolo", Name: "比克", Tier: "weak"},
	}
	v := confidentVerdict()
	v.ExecutorTier = "medium"
	v.ReviewerTier = "strong"
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "特兰克斯" {
		t.Fatalf("assigns = %v, want [特兰克斯] — every medium seat is tight", store.assigns)
	}
	// The reviewer must not be the seat that now does the work, even though
	// it serves both the medium and the strong rung.
	if len(store.reviewer) != 1 || store.reviewer[0] == "特兰克斯" {
		t.Errorf("reviewer = %v, must not be the executor", store.reviewer)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "上调一档") {
		t.Errorf("decision comment does not mention the upshift:\n%s", body)
	}
}

func TestRouteUpshiftStepDownDoesNotLandOnTheExecutor(t *testing.T) {
	store := newFakeStore()
	store.settings.AllowUpshift = true
	store.issue.ProjectName = ""
	// strong has one seat and it is ample; medium is all tight, so medium is
	// served by that same seat. A strong-on-strong collision with no second
	// family must not step down onto it again.
	store.roster = map[string]Agent{
		"贝吉塔": {ID: "a-vegeta", Name: "贝吉塔", Tier: "medium", Usage: UsageTight},
		"孙悟空": {ID: "a-goku", Name: "孙悟空", Tier: "strong", Usage: UsageAmple},
		"比克":  {ID: "a-piccolo", Name: "比克", Tier: "weak"},
	}
	v := confidentVerdict()
	v.ExecutorTier = "strong"
	v.ReviewerTier = "strong"
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Fatalf("assigns = %v, want [孙悟空]", store.assigns)
	}
	for _, name := range store.reviewer {
		if name == "孙悟空" {
			t.Errorf("reviewer = %v, the executor checked its own work", store.reviewer)
		}
	}
}
