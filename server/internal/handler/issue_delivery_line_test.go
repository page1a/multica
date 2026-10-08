package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// attachLineChild makes child a sub-issue of parent and opens its delivery
// line the way a capable daemon's claim does (DENE-1537).
func attachLineChild(t *testing.T, parentID, childID string) *service.DeliveryLineClaim {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE issue SET parent_issue_id = $2 WHERE id = $1`, childID, parentID); err != nil {
		t.Fatalf("attach child: %v", err)
	}
	claim := testHandler.claimDeliveryLine(ctx, util.MustParseUUID(childID), true)
	if claim == nil {
		t.Fatal("a sub-issue created after rollout should get a delivery line")
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_delivery_line WHERE issue_id = $1`, childID)
	})
	return claim
}

func pullStateDirect(t *testing.T, url string) string {
	t.Helper()
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM github_pull_request WHERE html_url = $1`, url).Scan(&state); err != nil {
		t.Fatalf("read PR state: %v", err)
	}
	return state
}

// A sub-issue on its parent's line closes done with the merge-back report as
// evidence: no PR of its own, and the parent's PR — linked to the child by
// its `Closes` line — is never merged by the child's close or pass.
func TestCloseLineChildMergesBackWithoutMergingParentPull(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := createIssueHTTP(t, "line parent", "in_progress")
	child := createIssueHTTP(t, "line child", "in_progress")
	claim := attachLineChild(t, parent.ID, child.ID)
	if !strings.HasPrefix(claim.Branch, "agent/delivery/") || claim.OwnerIssueID != parent.ID {
		t.Fatalf("claim = %+v", claim)
	}
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, child.ID, "running")
	parentPR := seedOpenPullForIssue(t, child.ID, 999537)
	calls := 0
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{calls: &calls}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	// Without the report the close is refused and says how to deliver.
	w := closeIssueHTTP(t, child.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "做完了"})
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "不要开 PR") {
		t.Fatalf("close without merge report: %d: %s", w.Code, w.Body.String())
	}
	assertCloseRejectedClean(t, child.ID, "in_progress")

	// A pass on the child has no PR of its own to merge.
	w = closeIssueHTTP(t, child.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "看过了", "verdict": "pass"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "父票") {
		t.Fatalf("pass on a line child: %d: %s", w.Code, w.Body.String())
	}

	w = closeIssueHTTP(t, child.ID, agentID, taskID, map[string]any{
		"outcome":  "done",
		"evidence": "本地测试全绿。",
		"delivery_merge": map[string]any{
			"status":        "merged",
			"branch":        claim.Branch,
			"source_branch": "agent/j/dene-2",
			"tip":           strings.Repeat("a", 40),
			"commits":       []map[string]string{{"sha": strings.Repeat("b", 40), "subject": "feat: stage one"}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("merged close: %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "done" || resp.Merged {
		t.Fatalf("status = %q merged = %v, want done without a merge", resp.Status, resp.Merged)
	}
	if calls != 0 || pullStateDirect(t, parentPR) != "open" {
		t.Fatalf("the child's close touched the parent's PR: calls = %d state = %s", calls, pullStateDirect(t, parentPR))
	}
	line, err := service.GetIssueDeliveryLine(context.Background(), testHandler.Queries, util.MustParseUUID(child.ID))
	if err != nil || line == nil || line.Status != service.DeliveryLineMerged {
		t.Fatalf("line after close = %+v, %v", line, err)
	}

	// The parent sees the child's commits; its own delivery reads them back.
	delivery := getIssueDeliveryHTTP(t, parent.ID)
	if len(delivery.Contributions) != 1 || len(delivery.Contributions[0].Commits) != 1 {
		t.Fatalf("parent contributions = %+v", delivery.Contributions)
	}
}

// A real conflict closes the child blocked with the files named; a sibling's
// line is untouched.
func TestCloseLineChildConflictBlocksWithFiles(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := createIssueHTTP(t, "line conflict parent", "in_progress")
	child := createIssueHTTP(t, "line conflict child", "in_progress")
	sibling := createIssueHTTP(t, "line conflict sibling", "in_progress")
	claim := attachLineChild(t, parent.ID, child.ID)
	attachLineChild(t, parent.ID, sibling.ID)
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, child.ID, "running")

	w := closeIssueHTTP(t, child.ID, agentID, taskID, map[string]any{
		"outcome":  "done",
		"evidence": "本地测试全绿。",
		"delivery_merge": map[string]any{
			"status":         "conflict",
			"branch":         claim.Branch,
			"source_branch":  "agent/j/dene-3",
			"commits":        []map[string]string{},
			"conflict_files": []string{"web/app.ts"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("conflict close: %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, child.ID); got != "blocked" {
		t.Fatalf("child status = %s, want blocked", got)
	}
	if got := issueMetaString(t, child.ID, blockwait.KeyWaitCondition); !strings.Contains(got, "web/app.ts") {
		t.Fatalf("wait condition = %q, want the conflict file", got)
	}
	if got := issueStatusDirect(t, sibling.ID); got != "in_progress" {
		t.Fatalf("sibling status = %s, want untouched", got)
	}
	line, _ := service.GetIssueDeliveryLine(context.Background(), testHandler.Queries, util.MustParseUUID(sibling.ID))
	if line == nil || line.Status != service.DeliveryLineOpen {
		t.Fatalf("sibling line = %+v", line)
	}
}

// An issue with no parent gets no line and keeps today's close.
func TestTopLevelIssueGetsNoDeliveryLine(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "line top level", "in_progress")
	if claim := testHandler.claimDeliveryLine(context.Background(), util.MustParseUUID(issue.ID), true); claim != nil {
		t.Fatalf("top-level claim = %+v, want none", claim)
	}
}

func getIssueDeliveryHTTP(t *testing.T, issueID string) service.IssueDelivery {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/issues/"+issueID+"/delivery", nil), "id", issueID)
	testHandler.GetIssueDelivery(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET delivery: %d: %s", w.Code, w.Body.String())
	}
	var out service.IssueDelivery
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return out
}
