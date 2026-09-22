package daemon

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/localdir"
)

// backfillLocalDirectoryIdentity reports missing real_path / repo_key /
// is_git_repo for directories this daemon holds. Best-effort and never on
// the assignment hot path's error return: a unique-index collision (two
// legacy spellings of one directory) must not fail the task (DENE-618).
func (d *Daemon) backfillLocalDirectoryIdentity(workspaceID string, chosen *localDirectoryAssignment, rest []*localDirectoryAssignment) {
	if d == nil || d.client == nil {
		return
	}
	all := make([]*localDirectoryAssignment, 0, 1+len(rest))
	if chosen != nil {
		all = append(all, chosen)
	}
	all = append(all, rest...)
	for _, a := range all {
		if a == nil || strings.TrimSpace(a.ResourceID) == "" {
			continue
		}
		if strings.TrimSpace(a.Ref.RealPath) != "" {
			continue
		}
		go d.reportLocalDirectoryIdentity(workspaceID, a)
	}
}

func (d *Daemon) reportLocalDirectoryIdentity(workspaceID string, a *localDirectoryAssignment) {
	if a == nil {
		return
	}
	probe := localdir.Probe(a.AbsPath)
	if !probe.Exists {
		return
	}
	body := map[string]any{}
	if strings.TrimSpace(a.Ref.RealPath) == "" && probe.RealPath != "" {
		body["real_path"] = probe.RealPath
	}
	if strings.TrimSpace(a.Ref.RepoKey) == "" && probe.RepoKey != "" {
		body["repo_key"] = probe.RepoKey
	}
	body["is_git_repo"] = probe.IsGitRepo
	if len(body) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := d.client.BackfillLocalDirectoryIdentity(ctx, a.ResourceID, body)
	if err != nil {
		slog.Warn("local_directory identity backfill failed",
			"resource_id", a.ResourceID,
			"workspace_id", workspaceID,
			"err", err)
		return
	}
	if resp.Conflict {
		slog.Warn("local_directory identity backfill skipped: unique conflict; keeping the first row",
			"resource_id", a.ResourceID,
			"workspace_id", workspaceID,
			"real_path", probe.RealPath,
			"local_path", a.AbsPath)
	}
}
