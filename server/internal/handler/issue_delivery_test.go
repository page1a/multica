package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A pass verdict on an issue with an unresolved rescue line does not merge:
// the issue goes to blocked with the delivery blocker as its wait condition,
// so the pass cannot silently pick one of two branches (DENE-820).
func TestAcceptancePassBlocksOnUnresolvedRescueLine(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "acceptance delivery guard", "in_review")
	agentID := handlerTestAgentID(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`,
		issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue_delivery_branch (issue_id, workspace_id, branch_name, role, agent_id)
		VALUES ($1, $2, 'agent/a/x', 'canonical', $3), ($1, $2, 'agent/b/x', 'rescue', $3)`,
		issue.ID, testWorkspaceID, agentID); err != nil {
		t.Fatalf("seed delivery rows: %v", err)
	}
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	testHandler.maybeReleaseOnAcceptance(ctx, loaded, db.Comment{
		AuthorType: "agent",
		AuthorID:   parseUUID(agentID),
		Content:    "可以合。\nverdict: pass\n",
		Type:       "comment",
	})
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "blocked" {
		t.Fatalf("status = %q, want blocked", status)
	}
	body, _, _, _ := systemCommentOn(t, issue.ID)
	if !strings.Contains(body, "agent/b/x") || !strings.Contains(body, "multica issue delivery") {
		t.Fatalf("block comment should name the rescue line and the command, got %s", body)
	}

	// Resolving the rescue line through the API clears the blocker.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issue.ID+"/delivery/classify", map[string]any{
		"branch": "agent/b/x", "role": "rescue", "resolution": "absorbed",
	})
	req = withURLParam(req, "id", issue.ID)
	testHandler.ClassifyIssueDeliveryBranch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("classify = %d: %s", w.Code, w.Body.String())
	}
	var d service.IssueDelivery
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Canonical == nil || d.Canonical.Branch != "agent/a/x" || len(d.Problems) != 0 {
		t.Fatalf("delivery after classify = %+v", d)
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/issues/"+issue.ID+"/delivery/classify", map[string]any{
		"branch": "agent/a/x", "role": "experiment",
	})
	req = withURLParam(req, "id", issue.ID)
	testHandler.ClassifyIssueDeliveryBranch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("classifying the canonical line = %d, want 400: %s", w.Code, w.Body.String())
	}
}
