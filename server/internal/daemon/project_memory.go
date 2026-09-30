package daemon

import (
	"context"
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
		if err := d.client.ReportProjectMemoryCheck(ctx, workspaceID, ProjectMemoryCheckRequest{
			ProjectID: target.ProjectID,
			Locations: results,
		}); err != nil {
			d.logger.Debug("project memory check report failed", "workspace_id", workspaceID, "project_id", target.ProjectID, "error", err)
		}
	}
}
