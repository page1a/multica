package closeprotocol

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/blockwait"
)

func audit() *KnowledgeAudit { return &KnowledgeAudit{None: true} }

func TestCheckRequestOrderAndMessages(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
		want string
	}{
		{"missing outcome", Request{Evidence: "x", Knowledge: audit()}, "缺 --outcome：" + OutcomeHelp()},
		{"unknown outcome", Request{Outcome: "finished", Evidence: "x", Knowledge: audit()}, "--outcome \"finished\" 不是收口结论；只能是 done / in_review / blocked / cancelled / backlog / todo / in_progress。" + OutcomeHelp()},
		{"missing evidence", Request{Outcome: "Done", Evidence: "  ", Knowledge: audit()}, "缺 --evidence：收口必须留证据（PR 链接、测试结论、或说明为什么done），一句话也行"},
		{"verdict hold", Request{Outcome: "done", Evidence: "x", Verdict: "hold", Knowledge: audit()}, VerdictRejectionMsg},
		{"in_progress alone", Request{Outcome: "in_progress", Evidence: "x", Knowledge: audit()}, ContinuationRequiredMsg},
		{"in_progress condition without timeout", Request{Outcome: "in_progress", Evidence: "x", Continuation: Continuation{WaitCondition: "等窗口"}, Knowledge: audit()}, ContinuationRequiredMsg},
		{"missing audit", Request{Outcome: "done", Evidence: "x"}, KnowledgeAuditRequiredMsg},
		{"accepted", Request{Outcome: " IN_PROGRESS ", Evidence: "x", Verdict: "", Continuation: Continuation{WakeAt: "2026-10-04T00:00:00Z"}, Knowledge: audit()}, ""},
		{"verdict pass", Request{Outcome: "done", Evidence: "x", Verdict: "PASS", Knowledge: audit()}, ""},
	} {
		if got := CheckRequest(tc.req); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := CheckRequest(Request{Outcome: "done", Evidence: "x", Knowledge: &KnowledgeAudit{Changes: []KnowledgeChange{{Location: "DESIGN.md", Summary: "x"}}}}); !strings.Contains(got, "不在项目记忆清单里") {
		t.Errorf("unknown knowledge location: %q", got)
	}
}

// The outcome table is written out in `multica issue close --help`, the
// multica-platform skill and the kun scheduling doc. This test pins each copy
// to Outcomes so a new, renamed or dropped outcome fails here instead of
// drifting (DENE-1183). DENE-1329 moved the table out of the runtime brief,
// which now names only `--outcome <...>` and leaves the list to `--help`.
func TestOutcomeTableMatchesWrittenCopies(t *testing.T) {
	enumeration := regexp.MustCompile(`--outcome <([a-z_|]+)>`)
	mention := regexp.MustCompile(`--outcome[ =]+([a-z_]+(?:\|[a-z_]+)*)`)
	for _, rel := range []string{
		"../../cmd/multica/cmd_issue.go",
		"../service/builtin_skills/multica-platform/references/close-protocol.md",
		"../../../docs/kun/scheduling-close-protocol.md",
	} {
		raw, err := os.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(raw)
		for _, m := range enumeration.FindAllStringSubmatch(text, -1) {
			if got := strings.Split(m[1], "|"); !slices.Equal(got, OutcomeNames()) {
				t.Errorf("%s: enumeration %v, want %v", rel, got, OutcomeNames())
			}
		}
		seen := map[string]bool{}
		for _, m := range mention.FindAllStringSubmatch(text, -1) {
			for _, name := range strings.Split(m[1], "|") {
				if !IsOutcome(name) {
					t.Errorf("%s: documents --outcome %s, which the server does not accept", rel, name)
				}
				seen[name] = true
			}
		}
		for _, m := range enumeration.FindAllStringSubmatch(text, -1) {
			for _, name := range strings.Split(m[1], "|") {
				seen[name] = true
			}
		}
		for _, name := range OutcomeNames() {
			if !seen[name] {
				t.Errorf("%s: never documents --outcome %s", rel, name)
			}
		}
	}
}

// Continuation.Present is the raw-field shape check the close gate runs
// before anything is parsed; blockwait.Record.Structured is the same rule on
// the stored record the block-wait gate builds. For every combination of
// well-formed fields the two must agree (DENE-1183).
func TestContinuationPresentMatchesBlockWaitStructured(t *testing.T) {
	values := []struct{ key, value string }{
		{blockwait.KeyBlockedBy, "DENE-1"},
		{blockwait.KeyWakeAt, "2026-10-04T00:00:00Z"},
		{blockwait.KeyWaitCondition, "CI green"},
		{blockwait.KeyWaitTimeout, "2026-10-05T00:00:00Z"},
		{blockwait.KeyNeedsHuman, "kun"},
	}
	for mask := 0; mask < 1<<len(values); mask++ {
		meta := map[string]any{}
		for i, v := range values {
			if mask&(1<<i) != 0 {
				meta[v.key] = v.value
			}
		}
		get := func(key string) string { s, _ := meta[key].(string); return s }
		c := Continuation{
			BlockedBy: get(blockwait.KeyBlockedBy), WakeAt: get(blockwait.KeyWakeAt),
			WaitCondition: get(blockwait.KeyWaitCondition), WaitTimeout: get(blockwait.KeyWaitTimeout),
			NeedsHuman: get(blockwait.KeyNeedsHuman),
		}
		if got, want := c.Present(), blockwait.ParseMetadata(meta).Structured(); got != want {
			t.Fatalf("fields %v: Continuation.Present=%v, blockwait Structured=%v", meta, got, want)
		}
	}
}
