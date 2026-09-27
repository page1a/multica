package daemon

import (
	"path/filepath"
	"testing"
)

func TestPullRequestReportDirSkipsRemovedWorktree(t *testing.T) {
	project := t.TempDir()
	gone := filepath.Join(t.TempDir(), "dene-899-removed")

	cases := []struct {
		name   string
		result TaskResult
		want   string
	}{
		{"removed worktree falls back to project checkout", TaskResult{WorkDir: gone, DurableWorkDir: project}, project},
		{"durable dir wins when both exist", TaskResult{WorkDir: t.TempDir(), DurableWorkDir: project}, project},
		{"plain checkout still uses workdir", TaskResult{WorkDir: project}, project},
		{"nothing on disk skips the report", TaskResult{WorkDir: gone}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullRequestReportDir(tc.result); got != tc.want {
				t.Fatalf("pullRequestReportDir = %q, want %q", got, tc.want)
			}
		})
	}
}
