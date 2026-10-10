package service

import (
	"testing"
	"time"
)

func TestDecideReviewSkip(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	const h1, h2, hB = "1111111aaaaaaaaa", "2222222bbbbbbbbb", "3333333ccccccccc"
	prA := ReviewSkipPR{URL: "https://github.com/o/r/pull/1", State: "merged", Head: h1, OpenedAt: t0}
	prB := ReviewSkipPR{URL: "https://github.com/o/r/pull/2", State: "merged", Head: hB, OpenedAt: t0.Add(2 * time.Hour)}
	approvedA := prA
	approvedA.ApprovedBy, approvedA.ApprovedAt, approvedA.ApprovedHead = "octo", t0.Add(time.Minute), h1
	approvedB := prB
	approvedB.ApprovedBy, approvedB.ApprovedAt, approvedB.ApprovedHead = "octo", t0.Add(3*time.Hour), hB
	// Same PR, a commit pushed after the review: what merged is H2.
	pushedA := prA
	pushedA.Head = h2
	approvedPushedA := approvedA
	approvedPushedA.Head = h2
	approvedNoHeadA := approvedA
	approvedNoHeadA.ApprovedHead = ""
	at := func(h time.Duration) time.Time { return t0.Add(h) }
	headsA := map[string]string{prKey(prA.URL): h1}
	seatPass := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "看过了\nverdict: pass", At: at(time.Hour), Heads: headsA}
	seatPassH2 := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: pass", At: at(5 * time.Hour), Heads: map[string]string{prKey(prA.URL): h2}}
	seatPassBoth := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: pass", At: at(4 * time.Hour),
		Heads: map[string]string{prKey(prA.URL): h1, prKey(prB.URL): hB}}
	seatPassNoHeads := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: pass", At: at(time.Hour)}
	seatHold := ReviewSkipVerdict{AuthorID: "seat", Seat: true, Content: "verdict: hold", At: at(time.Hour)}
	otherPass := ReviewSkipVerdict{AuthorID: "other", Content: "verdict: pass", At: at(4 * time.Hour), Heads: headsA}
	selfPass := ReviewSkipVerdict{AuthorID: "exec", Seat: true, Content: "verdict: pass", At: at(4 * time.Hour), Heads: headsA}
	executors := map[string]bool{"exec": true}

	for _, tc := range []struct {
		name     string
		prs      []ReviewSkipPR
		verdicts []ReviewSkipVerdict
		want     string
		wantURL  string
	}{
		{"no PR", nil, []ReviewSkipVerdict{seatPass}, "", ""},
		{"open PR", []ReviewSkipPR{{State: "open"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"merged and an open PR", []ReviewSkipPR{prA, {State: "open"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"closed unmerged", []ReviewSkipPR{{State: "closed"}}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"merged, not reviewed", []ReviewSkipPR{prA}, nil, "", ""},
		{"merged, seat passed", []ReviewSkipPR{prA}, []ReviewSkipVerdict{seatPass}, ReviewSkipPlatformVerdict, prA.URL},
		{"merged, a non-seat passed", []ReviewSkipPR{prA}, []ReviewSkipVerdict{otherPass}, "", ""},
		{"merged, the executor passed as seat", []ReviewSkipPR{prA}, []ReviewSkipVerdict{selfPass}, "", ""},
		{"non-seat pass newer than seat hold", []ReviewSkipPR{prA}, []ReviewSkipVerdict{otherPass, seatHold}, "", ""},
		{"seat hold is newest", []ReviewSkipPR{prA}, []ReviewSkipVerdict{seatHold, seatPass}, "", ""},
		{"merged and approved", []ReviewSkipPR{approvedA}, nil, ReviewSkipGitHubApprove, prA.URL},
		{"approved but seat hold", []ReviewSkipPR{approvedA}, []ReviewSkipVerdict{seatHold}, "", ""},
		{"merged plus closed duplicate", []ReviewSkipPR{{State: "closed"}, prA}, []ReviewSkipVerdict{seatPass}, ReviewSkipPlatformVerdict, prA.URL},
		// DENE-1678 review F3: A reviewed and merged, B only merged.
		{"A approved, B only merged", []ReviewSkipPR{approvedA, prB}, nil, "", ""},
		{"old pass, new PR after it", []ReviewSkipPR{prA, prB}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"pass after the new PR covers both", []ReviewSkipPR{prA, prB}, []ReviewSkipVerdict{seatPassBoth}, ReviewSkipPlatformVerdict, prB.URL},
		{"each PR approved", []ReviewSkipPR{approvedA, approvedB}, nil, ReviewSkipGitHubApprove, prB.URL},
		// DENE-1678 review F3, round 2: a review counts for the head it saw.
		{"pass on H1, H2 pushed and merged", []ReviewSkipPR{pushedA}, []ReviewSkipVerdict{seatPass}, "", ""},
		{"pass on H2 after the push", []ReviewSkipPR{pushedA}, []ReviewSkipVerdict{seatPassH2, seatPass}, ReviewSkipPlatformVerdict, prA.URL},
		{"approval on H1, H2 merged", []ReviewSkipPR{approvedPushedA}, nil, "", ""},
		{"approval on H1, H2 merged, seat passed H2", []ReviewSkipPR{approvedPushedA}, []ReviewSkipVerdict{seatPassH2}, ReviewSkipPlatformVerdict, prA.URL},
		{"approval with no known head", []ReviewSkipPR{approvedNoHeadA}, nil, "", ""},
		{"pass with no recorded heads", []ReviewSkipPR{prA}, []ReviewSkipVerdict{seatPassNoHeads}, "", ""},
		{"merged PR with no known head", []ReviewSkipPR{{URL: prA.URL, State: "merged"}}, []ReviewSkipVerdict{seatPass}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skip, ok := DecideReviewSkip(tc.prs, tc.verdicts, executors)
			if got := map[bool]string{true: skip.Kind}[ok]; got != tc.want {
				t.Fatalf("kind = %q (ok=%v), want %q", skip.Kind, ok, tc.want)
			}
			if ok && skip.PRURL != tc.wantURL {
				t.Fatalf("pr url = %q, want %q", skip.PRURL, tc.wantURL)
			}
		})
	}
}

func TestSameHead(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"abcdef1234", "abcdef1234", true},
		{"ABCDEF1", "abcdef1234", true},
		{"abcdef1234", "abcdef9999", false},
		{"", "", false},
		{"abc", "abcdef1234", false},
	} {
		if got := sameHead(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameHead(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestReviewSkipReason(t *testing.T) {
	r := ReviewSkip{Kind: ReviewSkipGitHubApprove, By: "octo", PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，octo 在 GitHub 上批准过" {
		t.Fatalf("reason = %q", got)
	}
	r = ReviewSkip{Kind: ReviewSkipPlatformVerdict, By: "布尔玛", PRURL: "u"}
	if got := r.Reason(); got != "PR u 已合入，验收席 布尔玛 在票上给过审查通过" {
		t.Fatalf("reason = %q", got)
	}
}
