package receipt

import (
	"strings"
	"testing"
)

func TestMarkdownCarriesConclusionPRAndKnowledge(t *testing.T) {
	r := Receipt{
		IssueID: "id-1", Identifier: "DENE-9", Title: "修登录", Status: "done",
		Summary:   "登录改走新令牌",
		PRs:       []PR{{Number: 12, URL: "https://github.com/o/r/pull/12", State: "merged"}},
		Knowledge: "AGENTS.md：令牌规则",
	}
	md := r.Markdown()
	for _, want := range []string{"[DENE-9](mention://issue/id-1)", "已完成", "结论：登录改走新令牌", "[#12](https://github.com/o/r/pull/12)", "沉淀：AGENTS.md：令牌规则"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown misses %q:\n%s", want, md)
		}
	}
}

func TestMarkdownOmitsEmptySections(t *testing.T) {
	md := Receipt{IssueID: "id-1", Identifier: "DENE-9", Title: "修登录", Status: "blocked"}.Markdown()
	for _, absent := range []string{"结论", "PR：", "沉淀"} {
		if strings.Contains(md, absent) {
			t.Fatalf("markdown has empty section %q:\n%s", absent, md)
		}
	}
	if !strings.Contains(md, "卡住了") || strings.Contains(md, "修登录") {
		t.Fatalf("markdown wants the status but not the chip's title:\n%s", md)
	}
}

func TestLineIsOneLine(t *testing.T) {
	line := Receipt{Identifier: "DENE-9", Title: "修登录", Status: "in_review", Summary: "第一行\n第二行",
		PRs: []PR{{Number: 3, URL: "https://x/pull/3", State: "open"}}}.Line()
	if strings.Contains(line, "\n") {
		t.Fatalf("line wraps: %q", line)
	}
	for _, want := range []string{"DENE-9 修登录 — 待验收", "第一行 第二行", "https://x/pull/3"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line misses %q: %q", want, line)
		}
	}
}

func TestReportable(t *testing.T) {
	for status, want := range map[string]bool{"done": true, "cancelled": true, "blocked": true, "in_review": true, "in_progress": false, "todo": false, "backlog": false} {
		if got := Reportable(status); got != want {
			t.Errorf("Reportable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("一二三四五", 3); got != "一二…" {
		t.Fatalf("Clip = %q", got)
	}
	if got := Clip(" a\n b ", 10); got != "a b" {
		t.Fatalf("Clip = %q", got)
	}
}

func TestMarkdownListsChildren(t *testing.T) {
	r := Receipt{IssueID: "p", Identifier: "DENE-1", Status: "done", Children: []Receipt{
		{IssueID: "c1", Identifier: "DENE-2", Status: "done", Summary: "接口做完。", PRs: []PR{{Number: 5, URL: "https://x/pull/5", State: "merged"}}},
		{IssueID: "c2", Identifier: "DENE-3", Status: "cancelled"},
		{IssueID: "c3", Identifier: "DENE-4", Status: "done", Summary: "界面做完", Knowledge: "AGENTS.md：新规则"},
	}}
	md := r.Markdown()
	for _, want := range []string{
		"子任务回执：",
		"- [DENE-2](mention://issue/c1) 已完成：接口做完；PR [#5](https://x/pull/5) 已合并",
		"- [DENE-3](mention://issue/c2) 已取消",
		"- [DENE-4](mention://issue/c3) 已完成：界面做完；沉淀 AGENTS.md：新规则",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown misses %q:\n%s", want, md)
		}
	}
}

func TestDigestCountsPastLimit(t *testing.T) {
	children := make([]Receipt, MaxChildren+3)
	for i := range children {
		children[i] = Receipt{IssueID: "c", Identifier: "DENE-9", Status: "done"}
	}
	d := Digest(children)
	if got := strings.Count(d, "\n- [DENE-9]"); got != MaxChildren {
		t.Fatalf("listed %d children, want %d", got, MaxChildren)
	}
	if !strings.Contains(d, "另有 3 个子任务") {
		t.Fatalf("digest misses the count:\n%s", d)
	}
	if Digest(nil) != "" {
		t.Fatal("empty digest must be empty")
	}
}
