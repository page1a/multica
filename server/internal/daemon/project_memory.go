package daemon

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/projectmemory"
)

// refreshProjectMemory performs the only local filesystem work in the memory
// feature: read-only stat calls against project local directories. It is
// best-effort so an older server or a temporarily unavailable control plane
// never prevents normal daemon registration and task execution.
func (d *Daemon) refreshProjectMemory(ctx context.Context, workspaceID string) {
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	targets, err := d.client.GetWorkspaceMemoryTargets(refreshCtx, workspaceID)
	if err != nil {
		d.logger.Debug("project memory target refresh failed", "workspace_id", workspaceID, "error", err)
		return
	}
	d.refreshProjectMemoryTargets(refreshCtx, workspaceID, targets.Projects)
}

func (d *Daemon) refreshProjectMemoryTargets(ctx context.Context, workspaceID string, projectTargets []ProjectMemoryTarget) {
	for _, target := range projectTargets {
		if strings.TrimSpace(target.Path) == "" ||
			(strings.TrimSpace(target.DaemonID) != "" && target.DaemonID != d.cfg.DaemonID) {
			continue
		}
		results := projectmemory.Check(target.Path)
		markMainlineMemory(ctx, target.Path, results)
		if err := d.client.ReportProjectMemoryCheck(ctx, workspaceID, ProjectMemoryCheckRequest{
			ProjectID: target.ProjectID,
			Locations: results,
		}); err != nil {
			d.logger.Debug("project memory check report failed", "workspace_id", workspaceID, "project_id", target.ProjectID, "error", err)
		}
	}
}

// markMainlineMemory sets MainlineRef on every missing slot the remote
// mainline already has, so the server can say "sync the local directory"
// instead of asking an agent to write the file again (DENE-1660). It reads
// refs only and never fetches; a non-git directory or any git failure leaves
// the results untouched.
func markMainlineMemory(ctx context.Context, root string, results []projectmemory.LocationResult) {
	missing := false
	for _, result := range results {
		missing = missing || !result.Exists
	}
	if !missing {
		return
	}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ref := memoryMainlineRef(gitCtx, root)
	if ref == "" {
		return
	}
	for i := range results {
		if results[i].Exists {
			continue
		}
		object := ref + ":" + strings.TrimSuffix(results[i].Path, "/")
		if exec.CommandContext(gitCtx, "git", "-C", root, "cat-file", "-e", object).Run() == nil {
			results[i].MainlineRef = ref
		}
	}
}

// memoryMainlineRef is what a fast-forward would bring the directory to: the
// current branch's upstream, else the remote default branch.
func memoryMainlineRef(ctx context.Context, root string) string {
	for _, args := range [][]string{
		{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"},
		{"symbolic-ref", "--short", "refs/remotes/origin/HEAD"},
	} {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
		if ref := strings.TrimSpace(string(out)); err == nil && ref != "" {
			return ref
		}
	}
	return ""
}
