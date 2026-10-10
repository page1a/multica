package blockwait

import (
	"testing"
	"time"
)

// DENE-1647: a failed acceptance run with nothing after it is a stall, not a
// round to nudge. The first failure retries; repeated failures move seats.
func TestDecidePatrolCoversFailedReview(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	base := PatrolInput{Status: "in_review", Quiet: QuietAfter, Now: now, ReviewRunFailed: true, ReviewFailures: 1}

	if d := DecidePatrol(base); d.Action != ActionCoverReview || d.Force {
		t.Fatalf("first failure = %+v, want cover_review without force", d)
	}
	twice := base
	twice.ReviewFailures = 2
	if d := DecidePatrol(twice); d.Action != ActionCoverReview || !d.Force {
		t.Fatalf("second failure = %+v, want forced cover_review", d)
	}
	early := base
	early.Quiet = QuietAfter - time.Minute
	if d := DecidePatrol(early); d.Action == ActionCoverReview {
		t.Fatalf("before the quiet window = %+v, want no cover", d)
	}
	asked := base
	asked.ReviewAsked = true
	if d := DecidePatrol(asked); d.Action != ActionHold {
		t.Fatalf("ask already open = %+v, want hold", d)
	}
	human := base
	human.ReviewerHuman = true
	if d := DecidePatrol(human); d.Action == ActionCoverReview {
		t.Fatalf("human reviewer = %+v, must not be covered by a seat", d)
	}
	recent := base
	recent.HasLastPatrol, recent.LastPatrol = true, now.Add(-time.Minute)
	if d := DecidePatrol(recent); d.Action != ActionHold {
		t.Fatalf("patrolled a minute ago = %+v, want hold", d)
	}
	if set, _ := (Decision{Action: ActionCoverReview}).FollowUp(now, "", "", ""); set[KeyPatrolAt] == "" {
		t.Fatal("cover_review must stamp the patrol clock")
	}
}

// DENE-1678: a failed acceptance run on work already merged and reviewed is
// closed as passed instead of looking for another seat.
func TestDecidePatrolReleasesSkippableFailedReview(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	base := PatrolInput{Status: "in_review", Quiet: QuietAfter, Now: now, ReviewRunFailed: true, ReviewFailures: 1, ReviewSkip: "PR x 已合入"}
	if d := DecidePatrol(base); d.Action != ActionRelease {
		t.Fatalf("skippable failed review = %+v, want release", d)
	}
	human := base
	human.ReviewerHuman = true
	if d := DecidePatrol(human); d.Action == ActionRelease {
		t.Fatalf("human reviewer = %+v, a person still decides", d)
	}
	running := base
	running.ReviewRunFailed = false
	if d := DecidePatrol(running); d.Action == ActionRelease {
		t.Fatalf("no failed run = %+v, the patrol leaves the seat alone", d)
	}
}
