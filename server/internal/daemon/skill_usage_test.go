package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func toolUse(tool string, input map[string]any) agent.Message {
	return agent.Message{Type: agent.MessageToolUse, Tool: tool, Input: input}
}

func TestSkillUsageDetectorClaudeSkillTool(t *testing.T) {
	workDir := t.TempDir()
	d := newSkillUsageDetector([]string{"codebase-design", "implement"}, workDir, filepath.Join(workDir, ".claude", "skills"))

	if got := d.Observe(toolUse("Skill", map[string]any{"skill": "codebase-design"})); !reflect.DeepEqual(got, []string{"codebase-design"}) {
		t.Fatalf("Skill tool call: got %v", got)
	}
	// A plugin-qualified call still names the bound skill.
	if got := d.Observe(toolUse("Skill", map[string]any{"skill": "kun:implement"})); !reflect.DeepEqual(got, []string{"implement"}) {
		t.Fatalf("qualified Skill call: got %v", got)
	}
	// Unbound names are not a use.
	if got := d.Observe(toolUse("Skill", map[string]any{"skill": "end-work"})); got != nil {
		t.Fatalf("unbound skill: got %v", got)
	}
}

func TestSkillUsageDetectorCodexReadsSkillFile(t *testing.T) {
	workDir := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	d := newSkillUsageDetector([]string{"Code Review"}, workDir, execenv.SkillsDirPath(workDir, "codex"), filepath.Join(codexHome, "skills"))

	cmd := "sed -n '1,200p' " + filepath.Join(codexHome, "skills", "code-review", "SKILL.md")
	got := d.Observe(toolUse("exec_command", map[string]any{"cmd": []any{"bash", "-lc", cmd}}))
	if !reflect.DeepEqual(got, []string{"Code Review"}) {
		t.Fatalf("codex read of SKILL.md: got %v", got)
	}
}

func TestSkillUsageDetectorRelativeAndCollisionSlug(t *testing.T) {
	workDir := t.TempDir()
	d := newSkillUsageDetector([]string{"deploy"}, workDir, filepath.Join(workDir, ".claude", "skills"))

	got := d.Observe(toolUse("Bash", map[string]any{"command": "cat ./.claude/skills/deploy-multica/SKILL.md"}))
	if !reflect.DeepEqual(got, []string{"deploy"}) {
		t.Fatalf("relative read of a collision slug: got %v", got)
	}
}

func TestSkillUsageDetectorIgnoresRepositoryCopies(t *testing.T) {
	workDir := t.TempDir()
	d := newSkillUsageDetector([]string{"multica-platform"}, workDir, filepath.Join(workDir, ".claude", "skills"))

	for _, path := range []string{
		filepath.Join(workDir, "server", "internal", "service", "builtin_skills", "multica-platform", "SKILL.md"),
		"server/internal/service/builtin_skills/multica-platform/SKILL.md",
		filepath.Join(workDir, "vendor", ".claude", "skills", "multica-platform", "SKILL.md"),
		"vendor/.claude/skills/multica-platform/SKILL.md",
		// The skill's other files are reading material, not the skill itself.
		filepath.Join(workDir, ".claude", "skills", "multica-platform", "references", "issues.md"),
	} {
		if got := d.Observe(toolUse("Read", map[string]any{"file_path": path})); got != nil {
			t.Fatalf("%s: got %v, want no skill", path, got)
		}
	}
}

func TestSkillUsageDetectorReportsOncePerRun(t *testing.T) {
	workDir := t.TempDir()
	skillFile := filepath.Join(workDir, ".claude", "skills", "implement", "SKILL.md")
	d := newSkillUsageDetector([]string{"implement"}, workDir, filepath.Join(workDir, ".claude", "skills"))

	if got := d.Observe(toolUse("Skill", map[string]any{"skill": "implement"})); len(got) != 1 {
		t.Fatalf("first use: got %v", got)
	}
	if got := d.Observe(toolUse("Read", map[string]any{"file_path": skillFile})); got != nil {
		t.Fatalf("second use of the same skill: got %v", got)
	}
	// Results and prose never count, even when they quote the path.
	if got := (&skillUsageDetector{}).Observe(agent.Message{Type: agent.MessageToolResult, Output: skillFile}); got != nil {
		t.Fatalf("tool result: got %v", got)
	}
}

func TestSkillUsageDetectorNilIsInert(t *testing.T) {
	var d *skillUsageDetector
	if got := d.Observe(toolUse("Skill", map[string]any{"skill": "x"})); got != nil {
		t.Fatalf("nil detector: got %v", got)
	}
	if newSkillUsageDetector(nil, "") != nil {
		t.Fatal("no skills should build no detector")
	}
}

func TestBuildCommentPromptSelectedSkills(t *testing.T) {
	agentData := &AgentData{Skills: []SkillData{{ID: "skill-1", Name: "codebase-design"}}}

	t.Run("comment that picked a skill names it", func(t *testing.T) {
		out := buildCommentPrompt(Task{
			IssueID:               "issue-1",
			TriggerCommentID:      "comment-1",
			TriggerCommentContent: "[/codebase-design](slash://skill/skill-1) 结合源码看看",
			Agent:                 agentData,
		}, "claude")
		if !strings.Contains(out, "Explicitly selected skills:\n- codebase-design\n") {
			t.Fatalf("expected selected skills block, got:\n%s", out)
		}
	})

	t.Run("ordinary comment adds nothing", func(t *testing.T) {
		out := buildCommentPrompt(Task{
			IssueID:               "issue-1",
			TriggerCommentID:      "comment-1",
			TriggerCommentContent: "看看 /usr/local/bin 下的东西",
			Agent:                 agentData,
		}, "claude")
		if strings.Contains(out, "Explicitly selected skills") {
			t.Fatalf("unexpected selected skills block:\n%s", out)
		}
	})

	t.Run("skill not bound to the agent is ignored", func(t *testing.T) {
		out := buildCommentPrompt(Task{
			IssueID:               "issue-1",
			TriggerCommentID:      "comment-1",
			TriggerCommentContent: "[/other](slash://skill/not-bound)",
			Agent:                 agentData,
		}, "claude")
		if strings.Contains(out, "Explicitly selected skills") {
			t.Fatalf("unbound skill must not be named:\n%s", out)
		}
	})
}

func TestExecuteAndDrainAppendsSkillEventOncePerTask(t *testing.T) {
	t.Parallel()
	d, rec := newTranscriptRecorder(t)
	workDir := t.TempDir()
	detector := newSkillUsageDetector([]string{"implement"}, workDir, filepath.Join(workDir, ".claude", "skills"))
	var seq atomic.Int32
	// Two attempts share one detector, as a resume retry does.
	for attempt := 0; attempt < 2; attempt++ {
		messages := make(chan agent.Message, 2)
		messages <- agent.Message{Type: agent.MessageToolUse, Tool: "Skill", CallID: "s", Input: map[string]any{"skill": "implement"}}
		messages <- agent.Message{Type: agent.MessageToolResult, CallID: "s", Output: "loaded"}
		close(messages)
		results := make(chan agent.Result, 1)
		results <- agent.Result{Status: "completed"}
		close(results)
		backend := sessionBackend{session: &agent.Session{Messages: messages, Result: results}}
		if _, _, err := d.executeAndDrainDetectingSkills(context.Background(), backend, "p", agent.ExecOptions{},
			slog.Default(), "task-skill", "", detector, &seq); err != nil {
			t.Fatal(err)
		}
	}
	reported := rec.snapshot()
	slices.SortFunc(reported, func(a, b TaskMessageData) int { return a.Seq - b.Seq })
	var types []string
	for _, m := range reported {
		types = append(types, m.Type)
	}
	want := []string{"tool_use", "skill", "tool_result", "tool_use", "tool_result"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("transcript types = %v, want %v", types, want)
	}
	if reported[1].Tool != "implement" {
		t.Fatalf("skill event tool = %q, want implement", reported[1].Tool)
	}
}
