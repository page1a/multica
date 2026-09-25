package routing

import (
	"context"
	"strings"
	"testing"
)

// The canonical layer for the tier TAG: which rung a seat sits on comes from
// what a person tagged it with, and which rung a ticket wants comes from its
// labels. Both replace a guess with an answer, so the tests here are mostly
// about the judge NOT being asked.

func TestNormalizeTierAcceptsKeyAndLabel(t *testing.T) {
	l := DefaultLadder
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"strong", "strong", true},
		{"STRONG", "strong", true},
		{"强", "strong", true},
		{"最强", "strongest", true},
		{"  中  ", "medium", true},
		{"", "", true},
		{"超强", "", false},
		{"s", "", false},
	} {
		got, ok := l.NormalizeTier(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeTier(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRequestedTierReadsLabels(t *testing.T) {
	l := DefaultLadder
	for _, tc := range []struct {
		name   string
		labels []string
		want   string
		ok     bool
	}{
		{"none", []string{"bug", "前端"}, "", false},
		{"label", []string{"bug", "强"}, "strong", true},
		{"key", []string{"weak"}, "weak", true},
		{"same rung twice", []string{"强", "strong"}, "strong", true},
		// Two rungs is a contradiction. Choosing one of them silently would
		// make the ticket say one thing and the router do another.
		{"conflict", []string{"强", "弱"}, "", false},
		{"empty", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := l.RequestedTier(tc.labels)
			if got != tc.want || ok != tc.ok {
				t.Errorf("RequestedTier(%v) = (%q, %v), want (%q, %v)", tc.labels, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCandidatesComeFromTagsNotNames(t *testing.T) {
	l := DefaultLadder
	// A roster whose names say nothing about strength: the ladder's base names
	// appear nowhere, so any rung found here came from a tag.
	roster := map[string]Agent{
		"甲": {ID: "a", Name: "甲", Tier: "strongest"},
		"乙": {ID: "b", Name: "乙", Tier: "强"},
		"丙": {ID: "c", Name: "丙", Tier: "medium"},
		"丁": {ID: "d", Name: "丁"},
	}
	seats := l.Candidates("", roster)
	if len(seats) != 3 {
		t.Fatalf("got %d seats, want 3: %+v", len(seats), seats)
	}
	want := []string{"甲", "乙", "丙"}
	for i, s := range seats {
		if s.Name != want[i] {
			t.Errorf("seat %d = %q, want %q", i, s.Name, want[i])
		}
	}
	if seats[1].TierKey != "strong" {
		t.Errorf("a label tag resolved to %q, want the strong key", seats[1].TierKey)
	}
}

func TestCandidatesPreferTheDirectionSeatOnTheSameRung(t *testing.T) {
	l := DefaultLadder
	roster := map[string]Agent{
		"甲":   {ID: "a", Name: "甲", Tier: "strong"},
		"甲游戏": {ID: "ag", Name: "甲游戏", Tier: "strong"},
	}
	seats := l.Candidates("游戏", roster)
	if len(seats) != 1 || seats[0].Name != "甲游戏" {
		t.Fatalf("direction seat not preferred: %+v", seats)
	}
	if seats[0].Direction != "游戏" {
		t.Errorf("direction = %q, want 游戏", seats[0].Direction)
	}
	// No direction asked for: the undirected seat on the rung, not the
	// specialised one — a generic ticket must not land on a domain pack.
	generic := l.Candidates("", roster)
	if len(generic) != 1 || generic[0].Name != "甲" {
		t.Fatalf("undirected seat not preferred: %+v", generic)
	}
}

func TestUntaggedWorkspaceStillRoutesByName(t *testing.T) {
	// Nothing tagged: the ladder.json base names are the fallback, so a
	// workspace that has not opened the agent page yet keeps routing.
	l := DefaultLadder
	roster := map[string]Agent{
		"布尔玛": {ID: "a", Name: "布尔玛"},
		"孙悟空": {ID: "b", Name: "孙悟空"},
	}
	seats := l.Candidates("", roster)
	if len(seats) != 2 || seats[0].Name != "布尔玛" || seats[1].TierKey != "strong" {
		t.Fatalf("name fallback broken: %+v", seats)
	}
}

func TestTaggedSeatOverridesTheNameConvention(t *testing.T) {
	// 孙悟空 is the "strong" base name in ladder.json. Tagging it 弱 must move
	// it, otherwise the tag is decorative.
	l := DefaultLadder
	roster := map[string]Agent{
		"孙悟空": {ID: "b", Name: "孙悟空", Tier: "weak"},
	}
	seats := l.Candidates("", roster)
	if len(seats) != 1 {
		t.Fatalf("got %d seats, want 1: %+v", len(seats), seats)
	}
	if seats[0].TierKey != "weak" {
		t.Errorf("tier = %q, want weak — the tag lost to the name", seats[0].TierKey)
	}
}

func TestCandidatesSkipADemotedSeatWhenTheRungHasAnother(t *testing.T) {
	l := DefaultLadder
	roster := map[string]Agent{
		"甲游戏": {ID: "ag", Name: "甲游戏", Tier: "strong", Demoted: true},
		"乙":   {ID: "b", Name: "乙", Tier: "strong"},
	}
	seats := l.Candidates("游戏", roster)
	if len(seats) != 1 || seats[0].ID != "b" {
		t.Fatalf("demoted direction seat was still chosen: %+v", seats)
	}

	// Everyone on the rung is demoted: the ordinary direction pick stands.
	roster["乙"] = Agent{ID: "b", Name: "乙", Tier: "strong", Demoted: true}
	seats = l.Candidates("游戏", roster)
	if len(seats) != 1 || seats[0].ID != "ag" {
		t.Fatalf("a rung of only demoted seats must still produce one: %+v", seats)
	}
}

func TestDemotionFootnoteNamesTheSeatThatWasPassedOver(t *testing.T) {
	l := DefaultLadder
	roster := map[string]Agent{
		"孙悟饭": {ID: "gpt", Name: "孙悟饭", Tier: "strongest", Demoted: true},
		"布尔玛": {ID: "claude", Name: "布尔玛", Tier: "strongest"},
	}
	chosen := &Seat{ID: "claude", Name: "布尔玛", TierKey: "strongest"}
	note := DemotionFootnote(l, roster, chosen)
	if !strings.Contains(note, "孙悟饭") || !strings.Contains(note, "做成一单") {
		t.Fatalf("footnote = %q", note)
	}
	only := &Seat{ID: "gpt", Name: "孙悟饭", TierKey: "strongest"}
	delete(roster, "布尔玛")
	note = DemotionFootnote(l, roster, only)
	if !strings.Contains(note, "没有别的席位") {
		t.Fatalf("solo footnote = %q", note)
	}
}

func TestLabelledTicketIsAssignedWithoutAskingTheJudge(t *testing.T) {
	store := newFakeStore()
	// The reviewer slot already holds an answer, so only the executor
	// question is left for the judge.
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	store.issue.Labels = []string{"弱"}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if judge.callCount() != 0 {
		t.Errorf("asked the model %d times for a rung the ticket already names", judge.callCount())
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "weak" {
		t.Fatalf("executor = %+v, want the weak rung", out.ExecutorWritten)
	}
	if got := store.comments[KindAssignment]; len(got) != 1 || !strings.Contains(got[0], "标签") {
		t.Errorf("decision comment does not say the rung came from a label: %v", got)
	}
	if out.Mentioned {
		t.Error("mentioned somebody although the ticket was dispatched")
	}
}

func TestLabelBeatsTheJudgeWhenBothSpeak(t *testing.T) {
	store := newFakeStore()
	store.issue.Labels = []string{"最强"}
	// The judge would have picked the weak rung; the ticket says strongest.
	judge := &fakeJudge{verdict: confidentVerdict()}
	judge.verdict.ExecutorTier = "weak"

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "strongest" {
		t.Fatalf("executor = %+v, want the strongest rung the label asked for", out.ExecutorWritten)
	}
	// The reviewer slot is still the judge's question, so it was still asked.
	if judge.callCount() != 1 {
		t.Errorf("judge call count = %d, want 1 (reviewer question only)", judge.callCount())
	}
}

func TestAnUnknownLabelIsNotATier(t *testing.T) {
	store := newFakeStore()
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	store.issue.Labels = []string{"紧急", "前端"}
	judge := &fakeJudge{verdict: confidentVerdict()}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if judge.callCount() != 1 {
		t.Errorf("judge call count = %d, want 1 — labels naming no rung leave the question open", judge.callCount())
	}
}
