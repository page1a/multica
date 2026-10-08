package execenv

import (
	"strings"
	"testing"
)

// TestClassifyTask pins the precedence rule on classifyTask. All four
// kinds plus tiebreak cases for safety.
func TestClassifyTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ctx  TaskContextForEnv
		want taskKind
	}{
		{"chat", TaskContextForEnv{ChatSessionID: "c"}, kindChat},
		{"quick-create", TaskContextForEnv{QuickCreatePrompt: "p"}, kindQuickCreate},
		{"autopilot", TaskContextForEnv{AutopilotRunID: "r"}, kindAutopilotRunOnly},
		{"issue-comment-triggered", TaskContextForEnv{IssueID: "i", TriggerCommentID: "c"}, kindIssue},
		{"issue-assignment-triggered", TaskContextForEnv{IssueID: "i"}, kindIssue},
		{"issue-bare", TaskContextForEnv{}, kindIssue},
		{"tiebreak-chat-vs-quick", TaskContextForEnv{ChatSessionID: "c", QuickCreatePrompt: "p"}, kindChat},
		{"tiebreak-quick-vs-autopilot", TaskContextForEnv{QuickCreatePrompt: "p", AutopilotRunID: "r"}, kindQuickCreate},
		{"tiebreak-autopilot-vs-comment", TaskContextForEnv{AutopilotRunID: "r", IssueID: "i", TriggerCommentID: "c"}, kindAutopilotRunOnly},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyTask(tc.ctx); got != tc.want {
				t.Errorf("classifyTask: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTaskKindHasIssueContext pins the predicate that gates Project
// Context / Sub-issue Creation in the slim dispatcher.
func TestTaskKindHasIssueContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind taskKind
		want bool
	}{
		{kindIssue, true},
		{kindAutopilotRunOnly, false},
		{kindQuickCreate, false},
		{kindChat, false},
	}
	for _, tc := range cases {
		if got := tc.kind.hasIssueContext(); got != tc.want {
			t.Errorf("kind=%d hasIssueContext: got %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestBuildMetaSkillContentBriefContent pins that buildMetaSkillContent
// renders the (now sole) brief: the issue-read verb line of the Commands verb
// map (DENE-1329) is present, and neither the retired legacy verbose
// description nor the pre-verb-map `issue get` synopsis is.
func TestBuildMetaSkillContentBriefContent(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID:          "issue-1",
		TriggerCommentID: "comment-1",
		AgentName:        "Eve",
		AgentID:          "eve-1",
	})

	if !strings.Contains(out, "- `issue get | list | children` — read issues\n") {
		t.Errorf("brief is missing the issue-read verb line\n---\n%s", out)
	}
	if strings.Contains(out, "- `multica issue get <id> --output json` — full issue.") {
		t.Errorf("brief still carries the pre-verb-map `issue get` synopsis; flags live in `--help` (DENE-1329)\n---\n%s", out)
	}
	if strings.Contains(out, "Get full issue details.") {
		t.Errorf("brief still carries the retired legacy `issue get` description\n---\n%s", out)
	}
}

// TestBuildMetaSkillContentIssueBodyFormatting pins where the shared
// issue-body hierarchy rule and the title convention live. Quick-create, whose
// whole job is creating an issue, carries both in the brief; every other kind
// reads the same words from `multica issue create --help` (DENE-1329), so the
// brief must not carry a second copy there.
func TestBuildMetaSkillContentIssueBodyFormatting(t *testing.T) {
	t.Parallel()

	fixtures := map[string]TaskContextForEnv{
		"issue":        {IssueID: "i-1"},
		"autopilot":    {AutopilotRunID: "r-1"},
		"quick-create": {QuickCreatePrompt: "create an issue"},
		"chat":         {ChatSessionID: "c-1"},
	}

	for name, ctx := range fixtures {
		name, ctx := name, ctx
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := buildMetaSkillContent("codex", ctx)
			guidance := []string{
				"## Issue Body Formatting",
				"An issue title already serves as its H1.",
				// The rule covers BOTH surfaces: `description` is the CLI/API
				// field name, `body` the UI term — the alias is a cross-surface
				// mapping, not prose (MUL-5442 stage-1 review).
				"do not add a Markdown H1 (`# ...`) to an issue body or description",
				"start with prose or `##` subheadings",
				"Only add an H1 when the user specifically requests one",
				"## Title Style",
				"`{Project}: {what}`",
				"issue → 任务",
			}
			for _, want := range guidance {
				present := strings.Contains(out, want)
				if name == "quick-create" && !present {
					t.Errorf("quick-create brief is missing issue-body formatting guidance %q\n---\n%s", want, out)
				}
				if name != "quick-create" && present {
					t.Errorf("%s brief carries issue-body formatting guidance %q; it lives in `multica issue create --help` (DENE-1329)\n---\n%s", name, want, out)
				}
			}
		})
	}
}

// TestBuildMetaSkillContentSlimKindMatrix locks in which sections the
// slim brief emits per task kind, machine-checking the matrix documented
// on `buildMetaSkillContentSlim`. Heading is matched as a discrete line
// (preceded by newline + followed by newline) so inline references like
// "see ## Comment Formatting" do not trip the absence assertions.
//
// DENE-1329 renamed Background Task Safety → Background work and Available
// Commands → Commands, moved Issue Body Formatting / Title Style to
// quick-create only, and retired Comment Formatting, Attachments and the
// standalone CLI-only section (their rules moved to `--help` and the Commands
// intro). The retired headings stay in the table with no kind so a re-grown
// section fails here.
func TestBuildMetaSkillContentSlimKindMatrix(t *testing.T) {

	baseRepo := []RepoContextForEnv{{URL: "https://example.com/x.git", Description: "x"}}
	baseSkill := []SkillContextForEnv{{Name: "skill-x", Description: "x"}}

	type sectionCheck struct {
		heading  string
		mustHave map[taskKind]bool
	}
	allKinds := map[taskKind]bool{
		kindIssue: true, kindAutopilotRunOnly: true,
		kindQuickCreate: true, kindChat: true,
	}
	issueKinds := map[taskKind]bool{kindIssue: true}
	quickCreateOnly := map[taskKind]bool{kindQuickCreate: true}
	noKind := map[taskKind]bool{}
	checks := []sectionCheck{
		{"# Multica Agent Runtime", allKinds},
		{"## Background work", allKinds},
		{"## Agent Identity", allKinds},
		{"## Commands", allKinds},
		{"## Issue Body Formatting", quickCreateOnly},
		{"## Title Style", quickCreateOnly},
		{"### Workflow", allKinds},
		{"## Output", allKinds},
		// Retired by DENE-1329 — must not come back on any kind.
		{"## Background Task Safety", noKind},
		{"## Available Commands", noKind},
		{"### Core", noKind},
		{"## Important: Always Use the `multica` CLI", noKind},
		{"## Comment Formatting", noKind},
		{"## Attachments", noKind},
		{"## Repositories", map[taskKind]bool{
			kindIssue: true, kindAutopilotRunOnly: true, kindChat: true,
		}},
		{"## Instruction Precedence", issueKinds},
		{"## Sub-issue Creation", issueKinds},
		// Quick-create included: it used to be skipped here and carry its own
		// copy in issue_context.md, which nothing read. One index, one place.
		{"## Skills", allKinds},
		{"## Mentions", issueKinds},
	}

	fixtures := map[taskKind]TaskContextForEnv{
		kindChat: {ChatSessionID: "c-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindQuickCreate: {QuickCreatePrompt: "p", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindAutopilotRunOnly: {AutopilotRunID: "r-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
		kindIssue: {IssueID: "i-1", AgentName: "Eve", AgentID: "eve-1",
			Repos: baseRepo, AgentSkills: baseSkill},
	}

	for kind, ctx := range fixtures {
		out := buildMetaSkillContent("claude", ctx)
		for _, c := range checks {
			needle := "\n" + c.heading + "\n"
			firstLine := c.heading + "\n"
			present := strings.HasPrefix(out, firstLine) || strings.Contains(out, needle)
			want := c.mustHave[kind]
			if want && !present {
				t.Errorf("kind=%d: expected heading %q in slim brief", kind, c.heading)
			}
			if !want && present {
				t.Errorf("kind=%d: heading %q should NOT be in slim brief (matrix gating regression)", kind, c.heading)
			}
		}
	}
}

// TestBriefDueDateTeachesCalendarDayFormat pins the --due-date synopsis to
// the calendar-day format the server canonically accepts
// (util.ParseCalendarDate: YYYY-MM-DD; an RFC3339 value passes only at exact
// UTC midnight). MUL-5696 found the brief teaching `<RFC3339>` while the CLI
// help and the projects skill say YYYY-MM-DD, steering agents that computed a
// natural timestamp into 400s.
//
// DENE-1329 moved every flag synopsis into `multica issue create --help` /
// `issue update --help`, whose --due-date flag says "calendar day,
// YYYY-MM-DD". The brief now teaches no --due-date format at all; what this
// test keeps pinning is that it never teaches the wrong one, and that a
// re-grown synopsis comes back in the calendar-day form.
func TestBriefDueDateTeachesCalendarDayFormat(t *testing.T) {
	for name, ctx := range map[string]TaskContextForEnv{
		"issue":        {IssueID: "issue-1"},
		"quick-create": {QuickCreatePrompt: "create an issue"},
	} {
		out := buildMetaSkillContent("claude", ctx)
		if strings.Contains(out, "--due-date") && !strings.Contains(out, "--due-date <YYYY-MM-DD>") {
			t.Errorf("%s brief mentions --due-date without the calendar-day YYYY-MM-DD synopsis", name)
		}
		if strings.Contains(out, "--due-date <RFC3339>") {
			t.Errorf("%s brief still teaches --due-date <RFC3339>, which the server rejects except at UTC midnight (MUL-5696)", name)
		}
	}
}

// TestBriefOwnsAutopilotIssueCommandsGuard pins the guard's single emission
// point: the autopilot brief carries AutopilotIssueCommandsGuard, and the
// per-turn prompt defers to it (daemon.TestBuildPromptAutopilotRunOnly pins
// the deferral side). MUL-5696.
func TestBriefOwnsAutopilotIssueCommandsGuard(t *testing.T) {
	out := buildMetaSkillContent("claude", TaskContextForEnv{AutopilotRunID: "run-1"})
	if !strings.Contains(out, AutopilotIssueCommandsGuard) {
		t.Errorf("autopilot brief missing AutopilotIssueCommandsGuard — the per-turn prompt defers to this single emission point")
	}
}

// TestQuickCreateBriefOwnsRunAndOutputRules pins the brief as the single
// statement of how a quick-create run executes and what it prints (MUL-6984).
//
// The same five rules used to be written three times — the brief's Workflow
// section, the brief's ## Output section, and the per-turn prompt's "Output
// format" block — with two of the three copies in this very file. The brief is
// the copy that survives, because its own contract is that these guardrails
// hold "even if the user message is missing"; the per-turn message now renders
// only the field VALUES the modal picked.
func TestQuickCreateBriefOwnsRunAndOutputRules(t *testing.T) {
	t.Parallel()

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		QuickCreatePrompt: "create an issue about flaky tests",
		AgentName:         "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		// exactly one create, no retry — a retry would duplicate the issue
		"Run exactly one `multica issue create --output json`, then exit",
		"Never retry, even on a non-zero exit",
		"a retry duplicates it",
		// no issue to query, transition, or comment on
		"Do NOT call `multica issue get`, `multica issue status` or `multica issue comment add`",
		// the success line, and the reason it must not be scraped or
		// prefix-guessed: workspaces set their own issue prefix, so a
		// successful create must not read as failed
		"using `identifier` (or `id`) from the JSON",
		"Created <identifier-or-id>: <title>",
		"never a guessed prefix such as `MUL-`",
		// failure path
		"exit with that error as the only output",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quick-create brief missing rule %q\n---\n%s", want, out)
		}
	}

	// ## Output states where the output goes and how a FILE is delivered; the
	// rules themselves are stated once, above. A second copy here is what this
	// change removed.
	outputIdx := strings.Index(out, "## Output")
	if outputIdx < 0 {
		t.Fatalf("quick-create brief has no ## Output section\n---\n%s", out)
	}
	outputSection := out[outputIdx:]
	if !strings.Contains(outputSection, "**Delivering files here:**") {
		t.Errorf("## Output lost the file-delivery channel\n---\n%s", outputSection)
	}
	for _, banned := range []string{
		"Created <identifier-or-id>: <title>",
		"Do NOT call `multica issue comment add`",
	} {
		if strings.Contains(outputSection, banned) {
			t.Errorf("## Output restates workflow rule %q\n---\n%s", banned, outputSection)
		}
	}
}

// TestSlimQuickCreateAvailableCommands locks the minimal-variant content
// for quick-create's Commands section: `issue create` present with its
// `--help` pointer, every other verb of the full verb map absent (the hard
// guardrails forbid the call).
func TestSlimQuickCreateAvailableCommands(t *testing.T) {

	out := buildMetaSkillContent("codex", TaskContextForEnv{
		QuickCreatePrompt: "create an issue about flaky tests",
		AgentName:         "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Commands",
		"Use `multica issue create --output json`",
		"see `--help` for flags",
		"`--description-file ./description.md`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quick_create slim Commands missing %q", want)
		}
	}

	for _, banned := range []string{
		"multica issue get <id>",
		"multica issue comment list <issue-id>",
		"multica issue update <id>",
		"multica issue status <id> <status>",
		"multica issue comment add <issue-id>",
		"multica issue children <id>",
		"multica repo checkout <url>",
		// verb-map lines of the full Commands section (DENE-1329)
		"`issue get | list | children`",
		"`issue context <id>`",
		"`issue comment list | add`",
		"`issue close`",
		"`issue handoff",
		"`repo checkout <url>`",
		"### Squad maintenance",
		"multica squad member set-role",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("quick_create slim Commands should NOT advertise %q (hard guardrails forbid the call)", banned)
		}
	}
}

// TestBackgroundTaskSafetySlimHardPins asserts the brief's Background work
// section keeps its four model-only constraints (MUL-4140, MUL-5223, MUL-5274).
//
// DENE-1329 compressed the old "## Background Task Safety" section into one
// paragraph (ADR-0007). The pins were renegotiated to the new wording, one
// group per constraint, so a future trim cannot quietly drop any of them:
// no background-and-yield (the MUL-4091 mechanism), no waiting on CI with the
// full compound ban, the single persistent-service exception with its handoff
// triple, and never killing the daemon by name.
func TestBackgroundTaskSafetySlimHardPins(t *testing.T) {

	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID: "i-1", TriggerCommentID: "tc-1",
		AgentName: "Eve", AgentID: "eve-1",
	})

	for _, want := range []string{
		"## Background work",
		// 1. The platform fact and the no-background-and-yield rule.
		"Your run ends when your turn exits",
		"anything still running is orphaned and its result lost",
		"Never background work and yield",
		"block on results in the foreground",
		// 2. CI / external systems. The full compound ban, not its first item —
		// MUL-5223 made this a non-derivable boundary, so no member may be
		// silently dropped.
		"Don't wait on CI or external systems",
		"no `gh pr checks --watch`, `gh run watch` or sleep polls",
		"`multica issue close` handles CI",
		"\"Local tests pass; CI running: <PR link>\" is a complete hand-off",
		// 3. The one persistent-service exception and its handoff triple.
		"Only a service the user asked to keep running may outlive the turn",
		"detach it (durable logs, a recorded PID), verify it, and reply with URL, logs and how to stop it",
		"without a supervisor its survival is best-effort",
		// 2b. The CI-result carve-out, restored by DENE-1329 (sample C).
		"explicitly ask for the CI result, wait for it in ONE foreground `gh pr checks <pr> --watch`",
		// 4. Never kill the daemon.
		"Never kill `multica` by name",
		"stop only a PID you started",
		"never the daemon's (`multica daemon status --output json`)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slim Background work missing hardened pin %q\n---\n%s", want, out)
		}
	}
	// Exactly one persistent-service exception. `gh pr checks` appears twice:
	// once inside the ban, once as the single foreground wait allowed when the
	// trigger explicitly asks for the CI result (DENE-1329 kept sample C's
	// carve-out).
	if got := strings.Count(out, "may outlive the turn"); got != 1 {
		t.Errorf("slim brief must state the persistent-service exception exactly once, got %d\n---\n%s", got, out)
	}
	if got := strings.Count(out, "gh pr checks"); got != 2 {
		t.Errorf("`gh pr checks` must appear exactly twice (the ban and the explicit-request carve-out), got %d\n---\n%s", got, out)
	}
	// `gh run watch` may only appear as a banned command, never as the
	// section's example of how to wait properly.
	if strings.Contains(out, "e.g. `gh run watch`") {
		t.Errorf("slim Background work should not suggest waiting for external GitHub CI\n---\n%s", out)
	}
	// MUL-5274 review: with the persistent-service exception in the list, a
	// "The rules above ..." scoping sentence would sweep in work that is
	// precisely no longer run-owned after handoff.
	if strings.Contains(out, "The rules above") {
		t.Errorf("slim Background work must not reintroduce the ambiguous \"The rules above\" scoping sentence\n---\n%s", out)
	}
	if strings.Contains(out, "## Background Task Safety") {
		t.Errorf("retired heading \"## Background Task Safety\" re-grew (DENE-1329)\n---\n%s", out)
	}
}
