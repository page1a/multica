package daemon

import (
	"context"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/ghpr"
)

// reportLocalPullRequests mirrors PRs for a completed run without relying on a
// GitHub App. gh is deliberately invoked in a checkout of the same repository
// so its credentials are the ones used to push the delivery branch.
func (d *Daemon) reportLocalPullRequests(ctx context.Context, task Task, result TaskResult) error {
	branch := strings.TrimSpace(result.BranchName)
	dir := pullRequestReportDir(result)
	if branch == "" || dir == "" || task.WorkspaceID == "" {
		return nil
	}
	prs, err := ghpr.List(ctx, dir, "--head", branch)
	if err != nil || len(prs) == 0 {
		return err
	}
	return d.client.ReportDaemonPullRequests(ctx, task.WorkspaceID, prs)
}

// pullRequestReportDir picks a directory that still exists when the report
// runs. A local_directory worktree is already removed by then (WorkDir points
// at a deleted path, so every gh call failed with chdir); DurableWorkDir is the
// project checkout it came from, which shares the GitHub remote.
func pullRequestReportDir(result TaskResult) string {
	for _, dir := range []string{result.DurableWorkDir, result.WorkDir} {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}
