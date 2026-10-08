package main

import (
	"strings"
	"testing"
)

// TestIssueHelpCarriesMovedBriefRules pins the rules DENE-1329 moved out of
// the agent brief into `--help`. The brief now carries one line per verb and
// tells the agent to look flags up here, so a rule dropped from this text is
// gone from every agent's view.
func TestIssueHelpCarriesMovedBriefRules(t *testing.T) {
	cases := map[string]struct {
		long string
		want []string
	}{
		"issue comment add": {issueCommentAddCmd.Long, []string{
			"--content-file ./reply.md",
			"never /tmp or a shared path",
			"$LASTEXITCODE",
		}},
		"issue create": {issueCreateCmd.Long, []string{
			"{Project}: {what}",
			"The title already serves as the H1",
			"--description-file ./description.md",
		}},
		"issue close": {issueCloseCmd.Long, []string{
			"in one transaction",
			"refused with the\nmissing item named",
			"--outcome in_review   delivered, awaiting acceptance (top-level issues only;",
			"--verdict pass        acceptance seat only, with --outcome done",
			"--verdict hold",
			"who gets woken",
		}},
		"issue handoff": {issueHandoffCmd.Long, []string{
			"skips a\ntarget that already has an active run",
			"refused if a person holds the seat",
			"never a hand-written @mention",
		}},
		"issue summon": {issueSummonCmd.Long, []string{
			"A close with --needs-human already calls that person",
		}},
	}
	for name, tc := range cases {
		for _, want := range tc.want {
			if !strings.Contains(tc.long, want) {
				t.Errorf("`multica %s --help` lost moved brief rule %q\n---\n%s", name, want, tc.long)
			}
		}
	}
}
