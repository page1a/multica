package statecard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

func closeMeta(conclusion, status, at string) map[string]string {
	return map[string]string{
		closeprotocol.KeyConclusion:        conclusion,
		closeprotocol.KeyStatus:            status,
		closeprotocol.KeyEvidenceCommentID: "c-1",
		closeprotocol.KeyNextOwnerType:     "agent",
		closeprotocol.KeyNextOwnerID:       "a-2",
		closeprotocol.KeyWakeAction:        "route",
		closeprotocol.KeyWaitingOn:         "",
		closeprotocol.KeyAt:                at,
	}
}

func TestDeriveNowWithoutCloseRecord(t *testing.T) {
	now := DeriveNow(map[string]string{}, "todo")
	if now.Closed || now.Status != "todo" || now.Conclusion != "" {
		t.Fatalf("no close record must leave the close fields empty: %+v", now)
	}
}

func TestDeriveNowReadsCloseRecord(t *testing.T) {
	meta := closeMeta("blocked", "blocked", "2026-10-04T10:00:00Z")
	meta[blockwait.KeyWaitCondition] = "等 CI"
	now := DeriveNow(meta, "blocked")
	if !now.Closed || now.Conclusion != "blocked" || now.NextOwnerID != "a-2" || now.WaitCondition != "等 CI" || now.Stale {
		t.Fatalf("unexpected now: %+v", now)
	}
}

func TestDeriveNowMarksStaleRecordAndDropsWait(t *testing.T) {
	meta := closeMeta("blocked", "blocked", "2026-10-04T10:00:00Z")
	meta[blockwait.KeyWaitCondition] = "等 CI"
	now := DeriveNow(meta, "in_progress")
	if !now.Stale || now.WaitCondition != "" {
		t.Fatalf("a status move after the close must mark the record stale and drop its wait: %+v", now)
	}
	meta[closeprotocol.KeySuperseded] = "2026-10-04T10:00:00Z"
	if now := DeriveNow(meta, "blocked"); !now.Superseded || now.WaitCondition != "" {
		t.Fatalf("a superseded record must say so: %+v", now)
	}
}

func TestDeriveBatonFromClose(t *testing.T) {
	meta := closeMeta("awaiting_review", "in_review", "2026-10-04T10:00:00Z")
	b := DeriveBaton(meta, CloseNote{Summary: "PR 已开，等验收", ByType: "agent", ByID: "a-1"})
	if b == nil || b.Kind != "close" || b.Summary != "PR 已开，等验收" || b.CommentID != "c-1" || b.At != "2026-10-04T10:00:00Z" {
		t.Fatalf("unexpected baton: %+v", b)
	}
}

func TestDeriveBatonNewerHandoffWins(t *testing.T) {
	meta := closeMeta("continuing", "in_progress", "2026-10-04T10:00:00Z")
	meta[KeyHandoffAt] = "2026-10-04T11:00:00Z"
	meta[KeyHandoffSummary] = "前端做完，剩 CLI"
	meta[KeyHandoffTo] = "孙悟天"
	b := DeriveBaton(meta, CloseNote{Summary: "旧收尾"})
	if b == nil || b.Kind != "handoff" || b.Summary != "前端做完，剩 CLI" || b.To != "孙悟天" {
		t.Fatalf("a later handoff must win over the close: %+v", b)
	}
}

func TestDeriveBatonNewerCloseWins(t *testing.T) {
	meta := closeMeta("delivered", "done", "2026-10-04T12:00:00Z")
	meta[KeyHandoffAt] = "2026-10-04T11:00:00Z"
	meta[KeyHandoffSummary] = "前端做完，剩 CLI"
	b := DeriveBaton(meta, CloseNote{Summary: "全部完成"})
	if b == nil || b.Kind != "close" || b.Summary != "全部完成" {
		t.Fatalf("a later close must win over the handoff: %+v", b)
	}
}

func TestDeriveBatonHandoffOnly(t *testing.T) {
	meta := map[string]string{KeyHandoffAt: "2026-10-04T11:00:00Z", KeyHandoffSummary: "接着做"}
	if b := DeriveBaton(meta, CloseNote{}); b == nil || b.Kind != "handoff" {
		t.Fatalf("handoff without a close must still be the baton: %+v", b)
	}
	if b := DeriveBaton(map[string]string{}, CloseNote{}); b != nil {
		t.Fatalf("no record means no baton: %+v", b)
	}
}

func TestSummaryBelongsToClose(t *testing.T) {
	closeAt := "2026-10-04T10:00:00Z"
	at, _ := time.Parse(time.RFC3339, closeAt)
	if !SummaryBelongsToClose(at.Add(300*time.Millisecond), closeAt) {
		t.Fatal("a line written right after the close belongs to it")
	}
	if SummaryBelongsToClose(at.Add(-time.Hour), closeAt) {
		t.Fatal("an older line is a previous close's summary")
	}
}

func TestChooseAnchorPerCaller(t *testing.T) {
	run := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	comment := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	explicit := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	if a, s := ChooseAnchor(Caller{Type: "agent"}, nil, &run, &comment); a != AnchorLastRun || !s.Equal(run) {
		t.Fatalf("agent is measured from its previous run: %s %v", a, s)
	}
	if a, s := ChooseAnchor(Caller{Type: "member"}, nil, &run, &comment); a != AnchorLastComment || !s.Equal(comment) {
		t.Fatalf("person is measured from their last comment: %s %v", a, s)
	}
	if a, s := ChooseAnchor(Caller{Type: "agent"}, &explicit, &run, nil); a != AnchorExplicit || !s.Equal(explicit) {
		t.Fatalf("explicit since wins: %s %v", a, s)
	}
	if a, s := ChooseAnchor(Caller{Type: "agent"}, nil, nil, &comment); a != AnchorNone || s != nil {
		t.Fatalf("an agent with no previous run has no anchor: %s %v", a, s)
	}
}

func TestNormalizeDecisions(t *testing.T) {
	out, err := NormalizeDecisions([]string{" 用新表存拍板 ", "用新表存拍板", "手机端只做网页"}, 0)
	if err != nil || len(out) != 2 || out[0] != "用新表存拍板" {
		t.Fatalf("trim and dedupe: %v %v", out, err)
	}
	if _, err := NormalizeDecisions([]string{"  "}, 0); !errors.Is(err, ErrDecisionEmpty) {
		t.Fatalf("empty decision: %v", err)
	}
	if _, err := NormalizeDecisions([]string{strings.Repeat("字", MaxDecisionLen+1)}, 0); !errors.Is(err, ErrDecisionTooLong) {
		t.Fatalf("long decision: %v", err)
	}
	if _, err := NormalizeDecisions([]string{"a"}, MaxDecisions); err == nil {
		t.Fatal("a full list must refuse another decision")
	}
}

func TestThreadTitleAndFirstParagraph(t *testing.T) {
	if got := ThreadTitle("\n## 自动选派\n\n正文"); got != "自动选派" {
		t.Fatalf("title: %q", got)
	}
	if got := FirstParagraph("做完了\n第二行\n\n证据"); got != "做完了 第二行" {
		t.Fatalf("first paragraph: %q", got)
	}
}

func TestRenderCoversEverySection(t *testing.T) {
	card := Card{
		Identifier: "DENE-1",
		Goal:       Goal{Title: "做状态卡", FinishLine: []Check{{Description: "Go 测试", Status: "passed"}}},
		Decisions:  []Decision{{Text: "用新表存拍板"}},
		Now:        Now{Status: "in_review", Closed: true, Conclusion: "awaiting_review", ClosedAt: "2026-10-04T10:00:00Z"},
		Baton:      &Baton{Kind: "handoff", To: "孙悟天", Summary: "剩 CLI", At: "2026-10-04T11:00:00Z"},
		Changes:    Changes{Anchor: AnchorLastRun, Since: "2026-10-04T09:00:00Z", Threads: []Thread{{ThreadID: "t-1", Title: "能不能先做手机", NewCount: 2}}},
	}
	text := Render(card)
	for _, want := range []string{"DENE-1", "[x] Go 测试", "用新表存拍板", "awaiting_review", "交棒给 孙悟天", "剩 CLI", "能不能先做手机", "--thread t-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render misses %q:\n%s", want, text)
		}
	}
}

func TestRenderNamesSourceChat(t *testing.T) {
	card := Card{Identifier: "DENE-2", Goal: Goal{Title: "x"}}
	if strings.Contains(Render(card), "来源") {
		t.Fatal("card without a source renders a source line")
	}
	card.Source = &Source{ChatSessionID: "chat-1", ChatTitle: "Multica · 回执", Excerpt: "聊天派的票要回执"}
	text := Render(card)
	for _, want := range []string{"来源：聊天「Multica · 回执」", "multica chat history --session chat-1", "原话：聊天派的票要回执"} {
		if !strings.Contains(text, want) {
			t.Fatalf("render misses %q:\n%s", want, text)
		}
	}
}

func TestLatestSummary(t *testing.T) {
	closed := map[string]string{}
	for _, k := range closeprotocol.Keys {
		closed[k] = ""
	}
	closed[closeprotocol.KeyAt] = "2026-10-08T10:00:00Z"
	withHandoff := func(at string) map[string]string {
		m := map[string]string{KeyHandoffSummary: "交棒结论", KeyHandoffAt: at}
		for k, v := range closed {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name  string
		meta  map[string]string
		close string
		want  string
	}{
		{"nothing", map[string]string{}, "", ""},
		{"close summary", closed, "收尾结论", "收尾结论"},
		{"close without summary", closed, "", ""},
		{"newer handoff", withHandoff("2026-10-08T10:00:05Z"), "收尾结论", "交棒结论"},
		{"newer close without summary hides an older handoff", withHandoff("2026-10-08T09:00:00Z"), "", ""},
		{"handoff only", map[string]string{KeyHandoffSummary: "交棒结论", KeyHandoffAt: "2026-10-08T09:00:00Z"}, "", "交棒结论"},
		{"stored latest wins", map[string]string{KeyLatestSummary: "最新  结论", KeyHandoffSummary: "交棒结论", KeyHandoffAt: "2026-10-08T09:00:00Z"}, "", "最新  结论"},
		{"stored empty hides older lines", map[string]string{KeyLatestSummary: "", KeyHandoffSummary: "交棒结论", KeyHandoffAt: "2026-10-08T09:00:00Z"}, "收尾结论", ""},
	}
	for _, c := range cases {
		if got := LatestSummary(c.meta, c.close); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
