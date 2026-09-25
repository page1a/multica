package blockwait

import (
	"strings"
	"testing"
	"time"
)

func TestRecordRequiresOneKind(t *testing.T) {
	if (Record{}).Structured() {
		t.Fatal("empty record is not a block")
	}
	if !(Record{BlockedBy: []string{"DENE-806"}}).Structured() {
		t.Fatal("blocked_by should count")
	}
	if !(Record{HasWakeAt: true, WakeAt: time.Now()}).Structured() {
		t.Fatal("wake_at should count")
	}
	if (Record{WaitCondition: "publisher caught up"}).Structured() {
		t.Fatal("a condition without a deadline is not enough")
	}
	if !(Record{WaitCondition: "publisher caught up", HasWaitTimeout: true, WaitTimeout: time.Now()}).Structured() {
		t.Fatal("condition plus deadline should count")
	}
	if !(Record{NeedsHuman: "00000000-0000-0000-0000-000000000001"}).Structured() {
		t.Fatal("needs_human should count")
	}
}

func TestSuggestFromWaitingComment(t *testing.T) {
	got := SuggestFromComments([]string{"买入这步卡住，等 DENE-806 修好再继续。"})
	if len(got.BlockedBy) != 1 || got.BlockedBy[0] != "DENE-806" {
		t.Fatalf("suggestion = %#v", got)
	}
	if !strings.Contains(got.Hint, "--blocked-by DENE-806") {
		t.Fatalf("hint = %q", got.Hint)
	}
	if SuggestFromComments([]string{"没有提到票号"}).Hint != "" {
		t.Fatal("plain comment must not invent a blocker")
	}
}

func TestMergeRejectsBadClock(t *testing.T) {
	_, err := Merge(nil, Input{WakeAt: "tomorrow"})
	if err == nil {
		t.Fatal("expected wake_at error")
	}
}

func TestWaitingOnMetadataCountsAsStructured(t *testing.T) {
	r := ParseMetadata(map[string]any{"close.waiting_on": "DENE-806"})
	if !r.Structured() || r.BlockedBy[0] != "DENE-806" {
		t.Fatalf("record = %#v", r)
	}
}

func TestWakeIdempotency(t *testing.T) {
	if AlreadyWoken("DENE-1,DENE-2", "DENE-2") != true {
		t.Fatal("expected already woken")
	}
	if AlreadyWoken("DENE-80", "DENE-806") {
		t.Fatal("DENE-80 must not match DENE-806")
	}
	if MarkWoken("DENE-1", "DENE-1") != "DENE-1" {
		t.Fatal("mark must not duplicate")
	}
}

func TestPatrolWakesAtClockAndPicksUpUnstructured(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	due := DecidePatrol(PatrolInput{
		Status: "blocked",
		Record: Record{HasWakeAt: true, WakeAt: now.Add(-time.Minute)},
		Now:    now,
	})
	if due.Action != ActionWake {
		t.Fatalf("due wake_at action = %s", due.Action)
	}
	future := DecidePatrol(PatrolInput{
		Status: "blocked",
		Quiet:  2 * time.Hour,
		Record: Record{HasWakeAt: true, WakeAt: now.Add(10 * time.Minute)},
		Now:    now,
	})
	if future.Action != ActionHold {
		t.Fatalf("future wake_at must wait, got %s", future.Action)
	}
	bare := DecidePatrol(PatrolInput{
		Status: "blocked",
		Quiet:  QuietAfter,
		Now:    now,
	})
	if bare.Action != ActionWake {
		t.Fatalf("unstructured blocked action = %s", bare.Action)
	}
	open := DecidePatrol(PatrolInput{
		Status:   "blocked",
		Quiet:    2 * time.Hour,
		Record:   Record{BlockedBy: []string{"DENE-806"}},
		Blockers: []BlockerView{{Ref: "DENE-806", Status: "in_progress"}},
		Now:      now,
	})
	if open.Action != ActionHold {
		t.Fatalf("open blocker must hold, got %s", open.Action)
	}
	cleared := DecidePatrol(PatrolInput{
		Status:   "blocked",
		Record:   Record{BlockedBy: []string{"DENE-806"}},
		Blockers: []BlockerView{{Ref: "DENE-806", Status: "done"}},
		Now:      now,
	})
	if cleared.Action != ActionWake {
		t.Fatalf("cleared blocker action = %s", cleared.Action)
	}
}

func TestAcceptancePassIsAStandaloneMarker(t *testing.T) {
	if IsAcceptancePass("验收通过，等待合并流程。") {
		t.Fatal("prose that sounds like a pass must not merge")
	}
	if IsAcceptancePass("阻断：验收通过后没有合并，请修复") {
		t.Fatal("a rejection that quotes the spec must not merge")
	}
	if IsAcceptancePass("未通过") || IsAcceptancePass("驳回，验收通过两个字出现在理由里") {
		t.Fatal("未通过 and 驳回 must not merge")
	}
	if !IsAcceptancePass("看完了。\nverdict: pass\n") {
		t.Fatal("a standalone verdict line is a pass")
	}
	if IsAcceptancePass("正文里写 verdict: pass 但不单独成行") {
		t.Fatal("the marker has to be its own line")
	}
	if IsAcceptancePass("verdict: pass\nverdict: hold") {
		t.Fatal("hold wins when both markers are present")
	}
	if !LooksLikePassHint("验收通过，等待合并流程。") {
		t.Fatal("prose pass is a hint")
	}
	if LooksLikePassHint("阻断：验收通过后没有合并，请修复") {
		t.Fatal("a rejection must not be hinted as a pass")
	}
	round := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if PassInRound("verdict: pass\n", round.Add(-time.Hour), round) {
		t.Fatal("a pass from the previous round does not count")
	}
	if !PassInRound("verdict: pass\n", round.Add(time.Minute), round) {
		t.Fatal("a pass after this round started counts")
	}
	if PassInRound("verdict: pass\n", round.Add(time.Minute), time.Time{}) {
		t.Fatal("without a round start, history is not a pass")
	}
	body, err := AppendVerdict("可以合。", "pass")
	if err != nil || !IsAcceptancePass(body) {
		t.Fatalf("append = %q, %v", body, err)
	}
	if _, err := AppendVerdict("可以合。", "maybe"); err == nil {
		t.Fatal("unknown verdict must fail")
	}
}

func TestReleaseDoesNotStayInReview(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if DecideRelease(nil, now).Action != ReleaseDone {
		t.Fatal("no PR should close")
	}
	clean := DecideRelease([]PRSnapshot{{Number: 12, State: "open", Mergeable: "clean", Checks: "success"}}, now)
	if clean.Action != ReleaseMerge {
		t.Fatalf("clean PR action = %s", clean.Action)
	}
	dirty := DecideRelease([]PRSnapshot{{Number: 12, State: "open", Mergeable: "dirty", URL: "https://example/pull/12"}}, now)
	if dirty.Action != ReleaseBlock || !dirty.Record.Structured() {
		t.Fatalf("conflict = %#v", dirty)
	}
	if !strings.Contains(dirty.Reason, "不继续停在待验收") {
		t.Fatalf("reason = %q", dirty.Reason)
	}
}

func TestDecideCloseMergesGreenAndBlocksTheRest(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	if DecideClose(nil, now).Action != ReleaseDone {
		t.Fatal("no open PR should close")
	}
	merged := PRSnapshot{Number: 51, State: "merged", Mergeable: "clean", Checks: "SUCCESS"}
	if DecideClose([]PRSnapshot{merged}, now).Action != ReleaseDone {
		t.Fatal("an already merged PR should close")
	}
	clean := DecideClose([]PRSnapshot{{Number: 51, State: "open", Mergeable: "clean", Checks: "SUCCESS", URL: "https://example/pull/51"}}, now)
	if clean.Action != ReleaseMerge || !strings.Contains(clean.Reason, "再关票") {
		t.Fatalf("clean green PR = %#v", clean)
	}
	dirty := DecideClose([]PRSnapshot{{Number: 51, State: "open", Mergeable: "dirty", URL: "https://example/pull/51"}}, now)
	if dirty.Action != ReleaseBlock || !dirty.Record.Structured() || !strings.Contains(dirty.Reason, "不标完成") {
		t.Fatalf("conflict = %#v", dirty)
	}
	pending := DecideClose([]PRSnapshot{{Number: 51, State: "open", Mergeable: "clean", Checks: "PENDING"}}, now)
	if pending.Action != ReleaseBlock || !strings.Contains(pending.Record.WaitCondition, "还没出结果") {
		t.Fatalf("pending checks = %#v", pending)
	}
}

func TestStaleWaitDoesNotOpenANewBlock(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	stale, err := Accept(map[string]any{
		KeyWakeAt:          now.Add(-5 * time.Hour).Format(time.RFC3339),
		KeyBlockedBy:       "DENE-1",
		"close.waiting_on": "DENE-1",
	}, Input{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Structured() {
		t.Fatalf("consumed wait must not satisfy the gate: %#v", stale)
	}
	future, err := Accept(map[string]any{
		KeyWakeAt: now.Add(time.Hour).Format(time.RFC3339),
	}, Input{}, now)
	if err != nil || !future.Structured() || !future.HasWakeAt {
		t.Fatalf("a future clock is still a wait: %#v, %v", future, err)
	}
	named, err := Accept(map[string]any{KeyBlockedBy: "DENE-1"}, Input{BlockedBy: "DENE-806"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(named.BlockedBy) != 1 || named.BlockedBy[0] != "DENE-806" {
		t.Fatalf("only this request's blocker counts, got %#v", named.BlockedBy)
	}
}

func TestPastClockWakesOnce(t *testing.T) {
	now := time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)
	first := DecidePatrol(PatrolInput{
		Status:        "blocked",
		Record:        Record{HasWakeAt: true, WakeAt: now.Add(-5 * time.Hour)},
		Now:           now,
		LastPatrol:    now.Add(-31 * time.Minute),
		HasLastPatrol: true,
	})
	if first.Action != ActionWake || !first.ConsumeWakeAt || !first.MarkSegment {
		t.Fatalf("first wake = %#v", first)
	}
	later := DecidePatrol(PatrolInput{
		Status:        "blocked",
		SegmentNudged: true,
		Quiet:         61 * time.Minute,
		Now:           now.Add(61 * time.Minute),
		LastPatrol:    now,
		HasLastPatrol: true,
	})
	if later.Action != ActionHold {
		t.Fatalf("same segment woke again: %#v", later)
	}
	set, drop := first.FollowUp(now, "", "", "")
	if _, ok := set[KeySegmentNudged]; !ok {
		t.Fatal("wake must record the segment")
	}
	found := false
	for _, key := range drop {
		if key == KeyWakeAt {
			found = true
		}
	}
	if !found {
		t.Fatalf("wake must consume the clock, drop = %#v", drop)
	}
}

func TestInReviewNudgeIsOnceAndHumanIsCommentOnly(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	nudge := DecidePatrol(PatrolInput{
		Status:        "in_review",
		Quiet:         QuietAfter,
		Now:           now,
		ReviewerHuman: true,
	})
	if nudge.Action != ActionWake || !nudge.CommentOnly || !nudge.MarkReview {
		t.Fatalf("human nudge = %#v", nudge)
	}
	again := DecidePatrol(PatrolInput{
		Status:       "in_review",
		Quiet:        2 * time.Hour,
		ReviewNudged: true,
		Now:          now.Add(2 * time.Hour),
	})
	if again.Action != ActionHold {
		t.Fatalf("second review nudge = %#v", again)
	}
	agent := DecidePatrol(PatrolInput{Status: "in_review", Quiet: QuietAfter, Now: now})
	if agent.Action != ActionWake || agent.CommentOnly {
		t.Fatalf("agent reviewer should be woken once with a run: %#v", agent)
	}
	leftover := DecidePatrol(PatrolInput{
		Status: "in_review",
		Record: Record{HasWakeAt: true, WakeAt: now.Add(-time.Hour)},
		Now:    now,
	})
	if leftover.Action != ActionHold {
		t.Fatalf("a leftover clock must not page the reviewer: %#v", leftover)
	}
	if DecidePatrol(PatrolInput{Status: "in_review", ReleasedPass: true, Now: now}).Action != ActionRelease {
		t.Fatal("a pass recorded for this round still closes")
	}
}

func TestNeedsHumanDoesNotEnqueue(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	got := DecidePatrol(PatrolInput{
		Status: "blocked",
		Record: Record{HasWakeAt: true, WakeAt: now.Add(-time.Minute), NeedsHuman: "00000000-0000-0000-0000-000000000001"},
		Now:    now,
	})
	if got.Action != ActionWake || !got.CommentOnly {
		t.Fatalf("needs_human = %#v", got)
	}
}

func TestFailedChildIsDueInsideTheQuietWindow(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	rec := FailureWake(now, "下游运行失败，到点重新叫醒执行人", 1)
	if !rec.Structured() {
		t.Fatal("failure wake must be a structured block")
	}
	if rec.WakeAt.After(now.Add(QuietAfter)) {
		t.Fatalf("wake_at = %s, later than 30 minutes", rec.WakeAt)
	}
	parked := DecidePatrol(PatrolInput{Status: "in_progress", Record: rec, Now: now.Add(time.Minute), Quiet: time.Minute})
	if parked.Action != ActionHold {
		t.Fatalf("in_progress is invisible to the patrol: %#v", parked)
	}
	due := DecidePatrol(PatrolInput{Status: "blocked", Record: rec, Now: now.Add(time.Minute)})
	if due.Action != ActionWake {
		t.Fatalf("blocked child = %#v", due)
	}
}

func TestDownstreamNoticeDoesNotAskForRedispatch(t *testing.T) {
	got := DownstreamFailureNotice("DENE-806", "abc", "被平台中断")
	if strings.Contains(got, "mention://agent/") {
		t.Fatalf("notice must not wake an agent: %s", got)
	}
	if !strings.Contains(got, "不用你去重派") || !strings.Contains(got, "DENE-806") {
		t.Fatalf("notice = %s", got)
	}
}

func TestQuietReviewWithoutSeatIsSeatedNotNudged(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 10, 0, 0, time.UTC)
	seatless := DecidePatrol(PatrolInput{
		Status:        "in_review",
		Quiet:         QuietAfter,
		ReviewerEmpty: true,
		Now:           now,
	})
	if seatless.Action != ActionSeat {
		t.Fatalf("seatless in_review action = %s, want %s", seatless.Action, ActionSeat)
	}
	if seatless.MarkReview {
		t.Fatal("seating must not spend the one review nudge")
	}
	set, _ := seatless.FollowUp(now, "", "", "")
	if set[KeyPatrolAt] == "" {
		t.Fatal("seating must stamp the patrol clock so it is not retried every sweep")
	}

	nudgedBefore := DecidePatrol(PatrolInput{
		Status:        "in_review",
		Quiet:         2 * time.Hour,
		ReviewerEmpty: true,
		ReviewNudged:  true,
		Now:           now,
	})
	if nudgedBefore.Action != ActionSeat {
		t.Fatalf("an earlier nudge must not silence a seatless review, got %s", nudgedBefore.Action)
	}

	seated := DecidePatrol(PatrolInput{
		Status: "in_review",
		Quiet:  QuietAfter,
		Now:    now,
	})
	if seated.Action != ActionWake || !seated.MarkReview {
		t.Fatalf("seated in_review keeps the reviewer nudge, got %+v", seated)
	}

	fresh := DecidePatrol(PatrolInput{
		Status:        "in_review",
		Quiet:         5 * time.Minute,
		ReviewerEmpty: true,
		Now:           now,
	})
	if fresh.Action != ActionHold {
		t.Fatalf("a fresh seatless review still gets its 30 minutes, got %s", fresh.Action)
	}
}

// DENE-870: a child that keeps failing the same way is not re-woken every
// minute; the clock moves out with each failure in a row.
func TestFailureWakeBacksOff(t *testing.T) {
	now := time.Date(2026, 9, 24, 13, 48, 0, 0, time.UTC)
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0}, {1, 0}, {2, 5 * time.Minute}, {3, 15 * time.Minute}, {4, time.Hour}, {5, 4 * time.Hour}, {40, 4 * time.Hour},
	}
	for _, c := range cases {
		rec := FailureWake(now, "", c.failures)
		if !rec.HasWakeAt || !rec.WakeAt.Equal(now.Add(c.want)) {
			t.Fatalf("failures=%d wake_at=%v, want %v", c.failures, rec.WakeAt, now.Add(c.want))
		}
	}
	// A backed-off clock is structured, so the patrol holds instead of
	// treating the block as unexplained.
	rec := FailureWake(now, "", 3)
	d := DecidePatrol(PatrolInput{Now: now.Add(time.Minute), Status: "blocked", Record: rec, Quiet: time.Hour})
	if d.Action == ActionWake {
		t.Fatalf("patrol woke a backed-off child early: %+v", d)
	}
	d = DecidePatrol(PatrolInput{Now: now.Add(16 * time.Minute), Status: "blocked", Record: rec, Quiet: time.Hour})
	if d.Action != ActionWake {
		t.Fatalf("patrol did not wake once the backoff elapsed: %+v", d)
	}
}
