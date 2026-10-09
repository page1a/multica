package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintLinkedViewShowsProjectContext(t *testing.T) {
	var b bytes.Buffer
	printLinkedView(&b, map[string]any{
		"source": map[string]any{"name": "Acme"},
		"projects": []any{map[string]any{
			"id": "p1", "title": "Shared", "status": "in_progress", "done": 1, "total": 2,
			"description": "Line one\nLine two",
			"resources": []any{
				map[string]any{"type": "github_repo", "url": "https://github.com/acme/shared"},
				map[string]any{"type": "local_directory", "path": "/src/shared"},
			},
			"memory_line": "Project memory: map is `AGENTS.md`.",
		}},
		"issues": []any{},
	})
	got := b.String()
	for _, want := range []string{
		"    Line one\n    Line two\n",
		"    - github_repo: https://github.com/acme/shared\n",
		"    - local_directory: /src/shared\n",
		"    Project memory: map is `AGENTS.md`.\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q:\n%s", want, got)
		}
	}
}
