package stallaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProbe struct {
	linked, active, commented bool
	linkedErr, activeErr      error
	commentErr                error
	asked                     []string
}

func (p *fakeProbe) HasLinkedPR(context.Context) (bool, error) {
	p.asked = append(p.asked, "pr")
	return p.linked, p.linkedErr
}

func (p *fakeProbe) HasActiveRun(context.Context) (bool, error) {
	p.asked = append(p.asked, "run")
	return p.active, p.activeErr
}

func (p *fakeProbe) CommentedSince(context.Context, time.Time) (bool, error) {
	p.asked = append(p.asked, "comment")
	return p.commented, p.commentErr
}

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func ts(t time.Time) string { return t.Format(time.RFC3339) }

func TestReadParsesStringAndBoolMarkers(t *testing.T) {
	s := Read(map[string]any{KeyAction: " announced ", KeyCandidate: true})
	if s.Action != ActionAnnounced {
		t.Fatalf("action = %q", s.Action)
	}
	if !s.Marked() {
		t.Fatal("bool candidate marker was not read")
	}
	if !Read(map[string]any{KeyCandidate: "true"}).Marked() {
		t.Fatal("string candidate marker was not read")
	}
	if Read(map[string]any{KeyCandidate: "yes"}).Marked() {
		t.Fatal("only true marks a candidate")
	}
}

func TestJudgedForSkipsUnchangedActivity(t *testing.T) {
	activityAt := time.Date(2026, 10, 2, 12, 0, 0, 123456000, time.UTC)
	s := Read(map[string]any{KeyJudgedAt: activityAt.Format(time.RFC3339Nano)})
	if !s.JudgedFor(activityAt) {
		t.Fatal("a model decision for the current activity should be reused")
	}
	if s.JudgedFor(activityAt.Add(time.Second)) {
		t.Fatal("new activity must make the ticket eligible for a fresh judgment")
	}
	if Read(nil).JudgedFor(activityAt) {
		t.Fatal("a ticket never judged is eligible")
	}
}

func TestAnnounceRefusalsComeBeforeLookups(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ticket Ticket
		want   error
	}{
		{"done", Ticket{Status: "done"}, ErrTerminal},
		{"cancelled", Ticket{Status: "cancelled"}, ErrTerminal},
		{"paused", Ticket{Status: "todo", Meta: map[string]any{"close.conclusion": "deferred"}}, ErrPaused},
	} {
		probe := &fakeProbe{linkedErr: errors.New("db down")}
		_, err := Announce(context.Background(), tc.ticket, probe, "dup", "checked", now)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if len(probe.asked) != 0 {
			t.Fatalf("%s: refusal must not depend on a lookup, asked %v", tc.name, probe.asked)
		}
	}
}

func TestAnnounceRefusesLinkedPRAndReportsLookupFailure(t *testing.T) {
	_, err := Announce(context.Background(), Ticket{Status: "todo"}, &fakeProbe{linked: true}, "dup", "checked", now)
	if !errors.Is(err, ErrLinkedPR) {
		t.Fatalf("linked PR err = %v", err)
	}
	_, err = Announce(context.Background(), Ticket{Status: "todo"}, &fakeProbe{linkedErr: errors.New("db down")}, "dup", "checked", now)
	var rej Rejection
	if !errors.Is(err, ErrLookup) || errors.As(err, &rej) {
		t.Fatalf("lookup failure must be an internal error, not a refusal: %v", err)
	}
}

func TestAnnounceAlwaysAppendsArtifactCheck(t *testing.T) {
	got, err := Announce(context.Background(), Ticket{Status: "in_progress"}, &fakeProbe{}, "  重复票 ", "已核对关联 PR（0 个）", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "重复票；已核对关联 PR（0 个）。" {
		t.Fatalf("reason = %q", got.Reason)
	}
	want := Patch{KeyAction: ActionAnnounced, KeyAnnouncedAt: ts(now), KeyReviewUntil: ts(now.Add(AnnouncementFor)), KeyReason: got.Reason}
	assertPatch(t, got.Patch, want)
	if !got.ReviewUntil.Equal(now.Add(AnnouncementFor)) {
		t.Fatalf("review until = %v", got.ReviewUntil)
	}
}

func TestKeepOnlyInsideTheNoticeWindow(t *testing.T) {
	open := Ticket{Status: "todo", Meta: map[string]any{KeyAction: ActionAnnounced, KeyReviewUntil: ts(now.Add(time.Hour))}}
	patch, err := Keep(open, now)
	if err != nil || patch[KeyAction] != ActionKept {
		t.Fatalf("keep inside window: %v %v", patch, err)
	}
	closed := Ticket{Status: "todo", Meta: map[string]any{KeyAction: ActionAnnounced, KeyReviewUntil: ts(now.Add(-time.Second))}}
	if _, err := Keep(closed, now); !errors.Is(err, ErrReviewClosed) {
		t.Fatalf("keep after window: %v", err)
	}
	if _, err := Keep(Ticket{Status: "todo", Meta: map[string]any{KeyAction: ActionKept}}, now); !errors.Is(err, ErrNothingToKeep) {
		t.Fatalf("keep without notice: %v", err)
	}
}

func announcedTicket(status string, reviewUntil time.Time) Ticket {
	return Ticket{Status: status, Meta: map[string]any{
		KeyAction:      ActionAnnounced,
		KeyAnnouncedAt: ts(reviewUntil.Add(-AnnouncementFor)),
		KeyReviewUntil: ts(reviewUntil),
		KeyReason:      "重复票。",
	}}
}

func TestExpireWaitsUntilTheClockRunsOut(t *testing.T) {
	probe := &fakeProbe{}
	if got := Expire(context.Background(), announcedTicket("todo", now.Add(time.Minute)), probe, now); got.Verdict != ExpiryWait {
		t.Fatalf("open window verdict = %v", got.Verdict)
	}
	paused := announcedTicket("todo", now.Add(-time.Minute))
	paused.Meta["close.conclusion"] = "continuing"
	if got := Expire(context.Background(), paused, probe, now); got.Verdict != ExpiryWait {
		t.Fatalf("paused verdict = %v", got.Verdict)
	}
	if len(probe.asked) != 0 {
		t.Fatalf("a waiting announcement must not be probed: %v", probe.asked)
	}
}

// DENE-1176 invariant: an expired announcement is re-checked before it
// cancels, and any sign of resumed work (or a failed lookup) keeps it.
func TestExpireRechecksBeforeCancelling(t *testing.T) {
	expired := now.Add(-time.Minute)
	for _, tc := range []struct {
		name   string
		status string
		probe  *fakeProbe
		why    string
	}{
		{"backlog", "backlog", &fakeProbe{}, "公示期内票被放回待规划"},
		{"linked PR", "todo", &fakeProbe{linked: true}, "公示期内关联了 PR"},
		{"PR lookup failed", "todo", &fakeProbe{linkedErr: errors.New("x")}, "无法核对关联 PR"},
		{"active run", "todo", &fakeProbe{active: true}, "公示期内有运行在跑"},
		{"run lookup failed", "todo", &fakeProbe{activeErr: errors.New("x")}, "无法核对进行中的运行"},
		{"resumed comment", "in_progress", &fakeProbe{commented: true}, "公示期内有人或智能体继续推进"},
		{"comment lookup failed", "todo", &fakeProbe{commentErr: errors.New("x")}, "无法核对公示期内的评论"},
	} {
		got := Expire(context.Background(), announcedTicket(tc.status, expired), tc.probe, now)
		if got.Verdict != ExpiryKeep || got.Why != tc.why {
			t.Fatalf("%s: verdict=%v why=%q", tc.name, got.Verdict, got.Why)
		}
		assertPatch(t, got.Patch, Patch{KeyAction: ActionKept, KeyReason: tc.why + "，系统不会自动取消。"})
	}
}

func TestExpireCancelsQuietTicketAndRecordsUndo(t *testing.T) {
	got := Expire(context.Background(), announcedTicket("in_progress", now.Add(-time.Minute)), &fakeProbe{}, now)
	if got.Verdict != ExpiryCancel {
		t.Fatalf("verdict = %v", got.Verdict)
	}
	reason := "重复票。 公示到期无人保留，系统自动取消。"
	if got.Why != reason {
		t.Fatalf("why = %q", got.Why)
	}
	assertPatch(t, got.Patch, Patch{KeyAction: ActionCancelled, KeyPrevious: "in_progress", KeyRevertUntil: ts(now.Add(RevertFor)), KeyReason: reason})
}

func TestCompleteParentRecordsPreviousStatus(t *testing.T) {
	got := CompleteParent("blocked", now)
	if got[KeyAction] != ActionParent || got[KeyPrevious] != "blocked" || got[KeyRevertUntil] != ts(now.Add(RevertFor)) {
		t.Fatalf("patch = %v", got)
	}
}

// DENE-1176 invariant: Undo only restores while the ticket still carries
// the status the system wrote.
func TestUndoNeverOverwritesALaterStatusChange(t *testing.T) {
	cancelled := Ticket{Status: "cancelled", Meta: map[string]any{KeyAction: ActionCancelled, KeyPrevious: "todo", KeyRevertUntil: ts(now.Add(time.Hour))}}
	rev, err := Undo(cancelled, now)
	if err != nil || rev.Restore != "todo" || rev.Patch[KeyAction] != ActionRevoked {
		t.Fatalf("undo cancelled: %+v %v", rev, err)
	}
	moved := cancelled
	moved.Status = "in_progress"
	if _, err := Undo(moved, now); err == nil || !strings.Contains(err.Error(), "`in_progress`") {
		t.Fatalf("undo after a person moved the ticket must refuse: %v", err)
	}
	parent := Ticket{Status: "cancelled", Meta: map[string]any{KeyAction: ActionParent, KeyPrevious: "todo", KeyRevertUntil: ts(now.Add(time.Hour))}}
	if _, err := Undo(parent, now); err == nil {
		t.Fatal("parent completion writes done; a cancelled parent was moved by someone else")
	}
}

func TestUndoRefusals(t *testing.T) {
	if _, err := Undo(Ticket{Status: "done", Meta: map[string]any{KeyAction: ActionKept}}, now); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("nothing to undo: %v", err)
	}
	expired := Ticket{Status: "done", Meta: map[string]any{KeyAction: ActionParent, KeyPrevious: "todo", KeyRevertUntil: ts(now.Add(-time.Second))}}
	if _, err := Undo(expired, now); !errors.Is(err, ErrUndoExpired) {
		t.Fatalf("expired: %v", err)
	}
	missing := Ticket{Status: "done", Meta: map[string]any{KeyAction: ActionParent, KeyRevertUntil: ts(now.Add(time.Hour))}}
	if _, err := Undo(missing, now); !errors.Is(err, ErrMissingPrevious) {
		t.Fatalf("missing previous: %v", err)
	}
}

func assertPatch(t *testing.T, got, want Patch) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("patch = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("patch[%s] = %q, want %q", k, got[k], v)
		}
	}
}
