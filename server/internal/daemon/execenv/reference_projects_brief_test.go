package execenv

import (
	"strings"
	"testing"
)

func TestReferenceProjectsBrief(t *testing.T) {
	t.Parallel()

	var empty strings.Builder
	writeReferenceProjects(&empty, TaskContextForEnv{})
	if empty.Len() != 0 {
		t.Fatalf("no linked projects must render nothing, got %q", empty.String())
	}

	var b strings.Builder
	writeReferenceProjects(&b, TaskContextForEnv{
		ReferenceProjects: []ReferenceProjectForEnv{{
			Title:       "Shared",
			SourceName:  "Acme",
			Description: "Shared project brief",
			MemoryLine:  "Project memory: map is `AGENTS.md`.",
			Resources: []ReferenceResourceForEnv{
				{Type: "github_repo", URL: "https://github.com/acme/shared"},
				{Type: "local_directory", Path: "/src/here"},
				{Type: "local_directory", Path: "/src/gone", Missing: true},
			},
		}},
	})
	got := b.String()
	for _, want := range []string{
		"## Read-only Reference Projects",
		"never push",
		"### Shared (from Acme)",
		"Shared project brief",
		"Project memory: map is `AGENTS.md`.",
		"- github_repo: https://github.com/acme/shared",
		"- local_directory: `/src/here`\n",
		"- local_directory: `/src/gone` — not on this machine",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief missing %q:\n%s", want, got)
		}
	}
}
