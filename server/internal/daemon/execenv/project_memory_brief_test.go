package execenv

import (
	"strings"
	"testing"
)

func TestProjectMemoryBriefIsOneLine(t *testing.T) {
	t.Parallel()

	const body = "SEDIMENT FILE BODY that must stay out of the brief"
	line := "Project memory: map is `AGENTS.md`; missing: `CONTEXT.md`, `docs/adr/`. Open a listed file only when this task needs it."
	var b strings.Builder
	writeProjectContext(&b, TaskContextForEnv{
		Projects: []ProjectContextForEnv{{
			ID:          "p1",
			Title:       "Alpha",
			Description: "Owner notes.\n\n" + body,
			MemoryLine:  line + "\n" + body,
		}},
	})
	got := b.String()
	if strings.Count(got, "Project memory:") != 1 {
		t.Fatalf("memory lines = %d\n%s", strings.Count(got, "Project memory:"), got)
	}
	if strings.Contains(got, body) && strings.Count(got, body) != 1 {
		t.Fatalf("file body leaked beyond the project description:\n%s", got)
	}
	// The memory sentence itself is one physical line, even if the claim
	// smuggled a newline and a file body into the field.
	for _, physical := range strings.Split(got, "\n") {
		if strings.Contains(physical, "Project memory:") && strings.Contains(physical, body) {
			t.Fatalf("memory line absorbed a file body: %q", physical)
		}
	}
	if !strings.Contains(got, "Project memory: map is `AGENTS.md`; missing: `CONTEXT.md`, `docs/adr/`.") {
		t.Fatalf("brief missing the map line:\n%s", got)
	}
}

func TestProjectMemoryBriefOmitsEmptyLine(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	writeProjectContext(&b, TaskContextForEnv{
		ProjectID:          "p1",
		ProjectTitle:       "Alpha",
		ProjectDescription: "Owner notes.",
	})
	if strings.Contains(b.String(), "Project memory:") {
		t.Fatalf("empty memory line was injected:\n%s", b.String())
	}
}
