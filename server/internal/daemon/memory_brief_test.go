package daemon

import "testing"

func TestConvertProjectsForEnvCopiesMemoryLine(t *testing.T) {
	t.Parallel()

	const line = "Project memory: map is `AGENTS.md`; missing: none. Open a listed file only when this task needs it."
	got := convertProjectsForEnv([]ProjectContextData{{
		ID:         "p1",
		Title:      "Alpha",
		MemoryLine: line,
	}})
	if len(got) != 1 || got[0].MemoryLine != line {
		t.Fatalf("memory line = %#v", got)
	}
}
