package execenv

import (
	"fmt"
	"strings"
	"testing"
)

// The runtime brief is the first thing every run loads, on every turn, so its
// size is a cost paid by every task. These ceilings are the gate from
// docs/adr/0007-context-injection-principles.md: a change that makes a brief
// thicker has to fail here and be argued for, not slip in one paragraph at a
// time.
//
// maxBytes is the ceiling enforced today: the measured size of the minimal
// brief plus a small margin, so the brief cannot grow but small wording fixes
// still fit. targetBytes is where the slimming work is headed; when a slimming
// change lands, lower maxBytes to just above the new measured size, and once
// it reaches targetBytes drop the margin. Never raise maxBytes to make a
// failing change pass — move the content out of the brief instead (see the
// ADR's three questions). DENE-1329 brought issue and chat under their
// targets; the ratchet below still keeps each ceiling just above the measured
// size, so the gap to the target is not free room to grow into.
var briefSizeBudgets = []struct {
	name        string
	ctx         TaskContextForEnv
	maxBytes    int
	targetBytes int // 0 = no target set yet
}{
	{"issue", TaskContextForEnv{IssueID: "i-1", AgentID: "a-1", AgentName: "Agent"}, 6000, 8 << 10},
	{"chat", TaskContextForEnv{ChatSessionID: "c-1", AgentID: "a-1", AgentName: "Agent"}, 4000, 6 << 10},
	{"quick-create", TaskContextForEnv{QuickCreatePrompt: "create an issue", AgentID: "a-1", AgentName: "Agent"}, 5200, 0},
	{"autopilot", TaskContextForEnv{AutopilotRunID: "r-1", AgentID: "a-1", AgentName: "Agent"}, 3700, 0},
}

// TestBriefSizeBudget renders each task kind's brief from a minimal context —
// no project, no user instructions, no skills, no repositories — so what is
// measured is only the platform's own fixed text.
func TestBriefSizeBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range briefSizeBudgets {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			brief := buildMetaSkillContent("claude", tc.ctx)
			if len(brief) > tc.maxBytes {
				t.Errorf("%s brief is %d bytes, over the %d-byte ceiling (target %d).\n"+
					"Do not raise the ceiling: apply the three questions in docs/adr/0007-context-injection-principles.md "+
					"(can the server check it? is it only needed for one action? does it change every turn?) "+
					"and move the new text out of the brief.\nSection sizes:\n%s",
					tc.name, len(brief), tc.maxBytes, tc.targetBytes, briefSectionSizes(brief))
			}
		})
	}
}

// briefSectionSizes lists each ## / ### section's byte size, so a failing
// budget shows where the weight is and a ticket can record the baseline.
func briefSectionSizes(brief string) string {
	var out strings.Builder
	title, size := "(preamble)", 0
	flush := func() { fmt.Fprintf(&out, "  %6d  %s\n", size, title) }
	for _, line := range strings.SplitAfter(brief, "\n") {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			flush()
			title, size = strings.TrimSpace(strings.TrimLeft(line, "# ")), 0
		}
		size += len(line)
	}
	flush()
	return out.String()
}

// TestBriefSizeBudgetIsTight is the ratchet: a ceiling far above the measured
// size would let the brief grow into the slack unnoticed, so when a slimming
// change lands the ceiling above must come down with it.
func TestBriefSizeBudgetIsTight(t *testing.T) {
	t.Parallel()
	const maxSlack = 1200
	for _, tc := range briefSizeBudgets {
		brief := buildMetaSkillContent("claude", tc.ctx)
		if slack := tc.maxBytes - len(brief); slack > maxSlack {
			t.Errorf("%s: ceiling %d is %d bytes above the measured %d; lower maxBytes in briefSizeBudgets to just above %d",
				tc.name, tc.maxBytes, slack, len(brief), len(brief))
		}
	}
}
