package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestAgentBlockedWithoutStructureIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "block gate", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO comment (id, issue_id, workspace_id, author_type, author_id, content, type)
		 VALUES ($1, $2, $3, 'agent', $4, '买入卡住了，等 DENE-806 修好。', 'comment')`,
		dbid.NewV7(), issue.ID, testWorkspaceID, agentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{"status": "blocked"})
	req = withURLParam(req, "id", issue.ID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DENE-806") || !strings.Contains(w.Body.String(), "--blocked-by") {
		t.Fatalf("rejection should suggest the comment's issue, got %s", w.Body.String())
	}
}

func TestBlockedByWakesWaiterAndNotesBothIssues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	waited := createIssueHTTP(t, "block-by upstream", "in_progress")
	waiter := createIssueHTTP(t, "block-by waiter", "blocked")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, waiter.ID, "agent", agentID)
	setIssueMetadataString(t, waiter.ID, blockwait.KeyBlockedBy, waited.Identifier)

	updateIssueStatusHTTP(t, waited.ID, "done")

	waiterBody, _, _, _ := systemCommentOn(t, waiter.ID)
	if !strings.Contains(waiterBody, waited.Identifier) || !strings.Contains(waiterBody, "可以继续了") {
		t.Fatalf("waiter comment = %s", waiterBody)
	}
	if got := countPendingTasksForAgent(t, waiter.ID, agentID); got != 1 {
		t.Fatalf("pending tasks = %d, want 1", got)
	}
	sourceBody, _, _, _ := systemCommentOn(t, waited.ID)
	if !strings.Contains(sourceBody, "已经叫醒等待方") {
		t.Fatalf("source comment = %s", sourceBody)
	}

	updateIssueStatusHTTP(t, waited.ID, "in_progress")
	updateIssueStatusHTTP(t, waited.ID, "done")
	if got := countSystemCommentsOn(t, waiter.ID); got != 1 {
		t.Fatalf("second done must not wake again, comments = %d", got)
	}
}

func TestPatrolWakesUnstructuredBlockAndDueClock(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)

	bare := createIssueHTTP(t, "patrol bare", "blocked")
	setIssueAssigneeDirect(t, bare.ID, "agent", agentID)
	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET last_activity_at = now() - interval '2 hours', updated_at = now() - interval '2 hours' WHERE id = $1`,
		bare.ID); err != nil {
		t.Fatalf("age bare issue: %v", err)
	}
	loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(bare.ID))
	if err != nil {
		t.Fatalf("reload bare: %v", err)
	}
	if !testHandler.patrolOne(ctx, loaded) {
		t.Fatal("unstructured blocked issue was not woken")
	}
	if got := countPendingTasksForAgent(t, bare.ID, agentID); got != 1 {
		t.Fatalf("patrol tasks = %d, want 1", got)
	}

	clocked := createIssueHTTP(t, "patrol clock", "blocked")
	setIssueAssigneeDirect(t, clocked.ID, "agent", agentID)
	setIssueMetadataString(t, clocked.ID, blockwait.KeyWakeAt, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	loaded, err = testHandler.Queries.GetIssue(ctx, parseUUID(clocked.ID))
	if err != nil {
		t.Fatalf("reload clocked: %v", err)
	}
	if !testHandler.patrolOne(ctx, loaded) {
		t.Fatal("due wake_at was not woken")
	}
	body, _, _, _ := systemCommentOn(t, clocked.ID)
	if !strings.Contains(body, "复查时间") {
		t.Fatalf("clock comment = %s", body)
	}
	var wakeAt *string
	if err := testPool.QueryRow(ctx, `SELECT metadata->>'block.wake_at' FROM issue WHERE id = $1`, clocked.ID).Scan(&wakeAt); err != nil {
		t.Fatalf("read wake_at: %v", err)
	}
	if wakeAt != nil && *wakeAt != "" {
		t.Fatalf("wake_at still set after the wake: %s", *wakeAt)
	}
	loaded, err = testHandler.Queries.GetIssue(ctx, parseUUID(clocked.ID))
	if err != nil {
		t.Fatalf("reload consumed: %v", err)
	}
	if testHandler.patrolOne(ctx, loaded) {
		t.Fatal("the same clock woke a second time")
	}
}

type fakeMerger struct {
	err error
}

func (f fakeMerger) MergePullRequest(context.Context, int64, string, string, int) error {
	return f.err
}

func TestAcceptancePassMergesAndCloses(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "acceptance pass", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`,
		issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	testHandler.maybeReleaseOnAcceptance(context.Background(), loaded, db.Comment{
		AuthorType: "agent",
		AuthorID:   parseUUID(agentID),
		Content:    "可以合。\nverdict: pass\n",
		Type:       "comment",
	})
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "done" {
		t.Fatalf("status = %q, want done", status)
	}
}

func TestProsePassDoesNotMerge(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "prose pass", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`,
		issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	testHandler.maybeReleaseOnAcceptance(context.Background(), loaded, db.Comment{
		AuthorType: "agent",
		AuthorID:   parseUUID(agentID),
		Content:    "阻断：验收通过后没有合并，请修复",
		Type:       "comment",
	})
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "in_review" {
		t.Fatalf("status = %q, want in_review", status)
	}
}

func TestLeavingBlockedClearsTheWait(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "clear block", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issue.ID, map[string]any{
		"status":     "blocked",
		"blocked_by": "DENE-806",
	})
	req = withURLParam(req, "id", issue.ID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("block: status = %d, body = %s", w.Code, w.Body.String())
	}
	updateIssueStatusHTTP(t, issue.ID, "in_progress")
	var blockedBy *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>'block.blocked_by' FROM issue WHERE id = $1`, issue.ID).Scan(&blockedBy); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if blockedBy != nil && *blockedBy != "" {
		t.Fatalf("blocked_by survived leaving blocked: %s", *blockedBy)
	}
}

func setIssueMetadataString(t *testing.T, issueID, key, value string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/"+key, json.RawMessage(body))
	req = withURLParams(req, "id", issueID, "key", key)
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set %s: expected 200, got %d: %s", key, w.Code, w.Body.String())
	}
}

// TestPatrolSeatsEmptyReviewInsteadOfWakingExecutor is DENE-860 at 12:10: the
// 30-minute reminder used to land on the executor because the acceptance seat
// was empty. The patrol now fills the seat and starts it; the executor is not
// asked to review its own work.
func TestPatrolSeatsEmptyReviewInsteadOfWakingExecutor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	executor := ensureLadderAgent(t, "孙悟空")
	ensureLadderAgent(t, "孙悟天")

	ageIntoSeatlessReview := func(t *testing.T, issueID string) db.Issue {
		t.Helper()
		if _, err := testPool.Exec(ctx, `
			UPDATE issue SET status = 'in_review', reviewer_type = NULL, reviewer_id = NULL,
				metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object($2::text, '1'),
				last_activity_at = now() - interval '2 hours', updated_at = now() - interval '2 hours'
			WHERE id = $1`, issueID, blockwait.KeyWatched); err != nil {
			t.Fatalf("age into seatless review: %v", err)
		}
		loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		return loaded
	}

	t.Run("agent executor gets a seat, not a nudge", func(t *testing.T) {
		issue := createIssueHTTP(t, "seatless review", "in_progress")
		setIssueAssigneeDirect(t, issue.ID, "agent", executor)
		loaded := ageIntoSeatlessReview(t, issue.ID)
		if !testHandler.patrolOne(ctx, loaded) {
			t.Fatal("seatless in_review was not acted on")
		}
		if got := countPendingTasksForAgent(t, issue.ID, executor); got != 0 {
			t.Fatalf("executor tasks = %d, want 0: the patrol must not wake the executor", got)
		}
		var status, reviewerID string
		if err := testPool.QueryRow(ctx, `
			SELECT status, COALESCE(reviewer_id::text, '') FROM issue WHERE id = $1
		`, issue.ID).Scan(&status, &reviewerID); err != nil {
			t.Fatalf("read issue: %v", err)
		}
		if status != "in_review" || reviewerID == "" || reviewerID == executor {
			t.Fatalf("status = %q reviewer = %q, want in_review with a seat other than %s", status, reviewerID, executor)
		}
		if got := countPendingTasksForAgent(t, issue.ID, reviewerID); got != 1 {
			t.Fatalf("acceptance tasks = %d, want 1", got)
		}
		body, _, _, _ := systemCommentOn(t, issue.ID)
		if !strings.Contains(body, "验收席") || !strings.Contains(body, "验收已经开始") {
			t.Fatalf("comment = %s", body)
		}
	})

	t.Run("no seat possible becomes a block a person can see", func(t *testing.T) {
		issue := createIssueHTTP(t, "seatless review human executor", "in_progress")
		setIssueAssigneeDirect(t, issue.ID, "member", testUserID)
		loaded := ageIntoSeatlessReview(t, issue.ID)
		if !testHandler.patrolOne(ctx, loaded) {
			t.Fatal("seatless in_review was not acted on")
		}
		var status, needsHuman string
		if err := testPool.QueryRow(ctx, `
			SELECT status, COALESCE(metadata->>$2, '') FROM issue WHERE id = $1
		`, issue.ID, blockwait.KeyNeedsHuman).Scan(&status, &needsHuman); err != nil {
			t.Fatalf("read issue: %v", err)
		}
		if status != "blocked" || needsHuman != testUserID {
			t.Fatalf("status = %q needs_human = %q, want blocked pointing at the workspace owner %s", status, needsHuman, testUserID)
		}
		body, _, _, _ := systemCommentOn(t, issue.ID)
		if !strings.Contains(body, "补不上验收席") || !strings.Contains(body, "mention://member/"+testUserID) {
			t.Fatalf("comment = %s", body)
		}
	})
}
