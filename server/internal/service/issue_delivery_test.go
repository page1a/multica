package service

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (f *delegatedFailureFixture) insertBranchTask(t *testing.T, branch string) pgtype.UUID {
	t.Helper()
	var taskID pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, started_at, dispatched_at,
			originator_user_id, accountable_user_id, originator_source, branch_name
		)
		VALUES ($1, $2, $3, 'running', 0, now(), now(), $4, $4, 'direct_human', $5)
		RETURNING id`, f.worker, f.runtimeID, f.workerIssue, f.userID, branch).Scan(&taskID); err != nil {
		t.Fatalf("seed branch task: %v", err)
	}
	return taskID
}

// The first branch a task delivers for an issue becomes its canonical line;
// a later task on another branch is recorded unclassified and announced, so a
// second delivery line never appears silently (DENE-820).
func TestTaskCompletionRecordsCanonicalAndFlagsSecondLine(t *testing.T) {
	f, svc := seedDelegatedFailureFixture(t)
	ctx := context.Background()
	workerIssueID, _ := util.ParseUUID(f.workerIssue)
	issue, err := svc.Queries.GetIssue(ctx, workerIssueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	first := f.insertBranchTask(t, "agent/a/dene-1")
	if _, err := svc.CompleteTask(ctx, first, []byte(`{"ok":true}`), "", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask(first): %v", err)
	}
	canonical, err := svc.Queries.GetIssueCanonicalDeliveryBranch(ctx, issue.ID)
	if err != nil {
		t.Fatalf("no canonical after first completion: %v", err)
	}
	if canonical.BranchName != "agent/a/dene-1" || canonical.FirstTaskID != first {
		t.Fatalf("canonical = %+v", canonical)
	}

	// Same branch again: continues the line, no new row, no notice.
	again := f.insertBranchTask(t, "agent/a/dene-1")
	if _, err := svc.FailTask(ctx, again, "boom", "", "", "", "agent_error.process_failure", false, "", ""); err != nil {
		t.Fatalf("FailTask(again): %v", err)
	}

	second := f.insertBranchTask(t, "agent/b/dene-1")
	if _, err := svc.CompleteTask(ctx, second, []byte(`{"ok":true}`), "", "", "", false, "", ""); err != nil {
		t.Fatalf("CompleteTask(second): %v", err)
	}
	rows, err := svc.Queries.ListIssueDeliveryBranches(ctx, issue.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].Role != DeliveryRoleCanonical || rows[1].Role != DeliveryRoleUnclassified || rows[1].BranchName != "agent/b/dene-1" {
		t.Fatalf("rows = %+v", rows)
	}
	var notices int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND type = 'system' AND content LIKE '%delivery classify%'`, issue.ID).Scan(&notices); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	if notices != 1 {
		t.Fatalf("unclassified-line notices = %d, want exactly 1", notices)
	}

	d, err := BuildIssueDelivery(ctx, svc.Queries, issue)
	if err != nil {
		t.Fatalf("BuildIssueDelivery: %v", err)
	}
	if d.Canonical == nil || d.Canonical.Branch != "agent/a/dene-1" || len(d.Canonical.Tasks) != 2 {
		t.Fatalf("canonical view = %+v", d.Canonical)
	}
	if blocker := DeliveryMergeBlocker(d, nil); !strings.Contains(blocker, "agent/b/dene-1") {
		t.Fatalf("unclassified line must block the merge, got %q", blocker)
	}

	// Swap canonical: the old line becomes rescue, and until it is resolved
	// the merge stays blocked. Resolving it clears the blocker and, once the
	// issue is closed, puts it on the cleanup plan.
	if _, err := SetIssueCanonicalDeliveryBranch(ctx, svc.Queries, issue, "agent/b/dene-1"); err != nil {
		t.Fatalf("SetIssueCanonicalDeliveryBranch: %v", err)
	}
	d, _ = BuildIssueDelivery(ctx, svc.Queries, issue)
	if d.Canonical.Branch != "agent/b/dene-1" || d.Branches[1].Role != DeliveryRoleRescue {
		t.Fatalf("after swap: canonical=%s other=%+v", d.Canonical.Branch, d.Branches[1])
	}
	if blocker := DeliveryMergeBlocker(d, nil); !strings.Contains(blocker, "agent/a/dene-1") {
		t.Fatalf("unresolved rescue must block, got %q", blocker)
	}
	if _, err := ClassifyIssueDeliveryBranch(ctx, svc.Queries, issue, "agent/a/dene-1", DeliveryRoleRescue, DeliveryResolutionDiscarded); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if _, err := ClassifyIssueDeliveryBranch(ctx, svc.Queries, issue, "agent/b/dene-1", DeliveryRoleExperiment, ""); err == nil {
		t.Fatal("classifying the canonical line must be refused")
	}
	d, _ = BuildIssueDelivery(ctx, svc.Queries, issue)
	if blocker := DeliveryMergeBlocker(d, nil); blocker != "" {
		t.Fatalf("resolved rescue should not block, got %q", blocker)
	}
	if len(d.CleanupPlan) != 1 || d.CleanupPlan[0].Allowed {
		t.Fatalf("cleanup before merge must not be allowed: %+v", d.CleanupPlan)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE issue SET status = 'done' WHERE id = $1`, issue.ID); err != nil {
		t.Fatal(err)
	}
	issue, _ = svc.Queries.GetIssue(ctx, issue.ID)
	d, _ = BuildIssueDelivery(ctx, svc.Queries, issue)
	if !d.Merged || len(d.CleanupPlan) != 1 || !d.CleanupPlan[0].Allowed || d.CleanupPlan[0].Branch != "agent/a/dene-1" {
		t.Fatalf("cleanup plan after done = %+v (merged=%v)", d.CleanupPlan, d.Merged)
	}
	if _, err := RecordIssueDeliveryCleanup(ctx, svc.Queries, issue, "agent/a/dene-1", DeliveryCleanupCleaned, "removed"); err != nil {
		t.Fatalf("record cleanup: %v", err)
	}
	if _, err := RecordIssueDeliveryCleanup(ctx, svc.Queries, issue, "agent/b/dene-1", DeliveryCleanupCleaned, ""); err == nil {
		t.Fatal("cleaning the canonical line must be refused")
	}
	d, _ = BuildIssueDelivery(ctx, svc.Queries, issue)
	if d.CleanupPlan[0].Allowed || d.CleanupPlan[0].CleanupStatus != DeliveryCleanupCleaned {
		t.Fatalf("cleaned line still on plan: %+v", d.CleanupPlan[0])
	}
	_ = db.IssueDeliveryBranch{}
}
