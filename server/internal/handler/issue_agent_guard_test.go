package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func issueForGuardTest(t *testing.T, issueID string) db.Issue {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue %s: %v", issueID, err)
	}
	return issue
}

func seedDelegatedTask(t *testing.T, agentID, issueID string) string {
	t.Helper()
	return dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id":         handlerTestRuntimeID(t),
		"issue_id":           issueID,
		"status":             "completed",
		"completed_at":       testutil.Raw("now()"),
		"originator_source":  "delegation",
		"originator_user_id": testUserID,
		// agent_task_queue_accountable_matches_originator requires the pair to
		// agree whenever originator_user_id is set.
		"accountable_user_id": testUserID,
	})
}

func seedDelegatedTasks(t *testing.T, agentID, issueID string, n int) string {
	t.Helper()
	var first string
	for i := 0; i < n; i++ {
		id := seedDelegatedTask(t, agentID, issueID)
		if i == 0 {
			first = id
		}
	}
	return first
}

func snapshotHandlerTestWorkspaceSettings(t *testing.T) []byte {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT COALESCE(settings, '{}'::jsonb) FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, previous, testWorkspaceID)
	})
	return previous
}

func setHandlerTestAgentChainBudget(t *testing.T, budget int) {
	t.Helper()
	snapshotHandlerTestWorkspaceSettings(t)
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object('agent_chain_budget', $1::int)
		WHERE id = $2
	`, budget, testWorkspaceID)
}

func clearHandlerTestAgentChainBudget(t *testing.T) {
	t.Helper()
	snapshotHandlerTestWorkspaceSettings(t)
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = COALESCE(settings, '{}'::jsonb) - 'agent_chain_budget'
		WHERE id = $1
	`, testWorkspaceID)
}

func countBudgetNotices(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	dbfx.QueryRow(t, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'system'
		  AND content LIKE 'Agent delegation chain reached the limit%'
	`, issueID).Scan(&count)
	return count
}

func budgetNoticeParentID(t *testing.T, issueID string) string {
	t.Helper()
	var parentID string
	dbfx.QueryRow(t, `
		SELECT COALESCE(parent_id::text, '')
		FROM comment
		WHERE issue_id = $1 AND author_type = 'system'
		  AND content LIKE 'Agent delegation chain reached the limit%'
		ORDER BY created_at ASC
		LIMIT 1
	`, issueID).Scan(&parentID)
	return parentID
}

// setAgentChainBudget writes the workspace's chain budget as raw JSON and
// restores the previous settings afterwards. Used when the case needs an
// invalid or zero value, not just a positive integer override.
func setAgentChainBudget(t *testing.T, rawValue string) {
	t.Helper()
	snapshotHandlerTestWorkspaceSettings(t)
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{agent_chain_budget}', $2::jsonb)
		WHERE id = $1
	`, testWorkspaceID, rawValue)
}

// TestIssueAgentChainBudgetBlocksAtTheThresholdAndResetsOnHumanComment covers
// the complete A↔B guard contract: a configured budget of six delegated runs
// is consumed, the seventh is refused, the notice is one-shot and parented to
// the triggering comment, and a human comment resets the window so a later
// delegated trigger can enqueue again.
func TestIssueAgentChainBudgetBlocksAtTheThresholdAndResetsOnHumanComment(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setHandlerTestAgentChainBudget(t, 6)
	agentA := createHandlerTestAgent(t, "Guard Budget Agent A", []byte("[]"))
	agentB := createHandlerTestAgent(t, "Guard Budget Agent B", []byte("[]"))
	// Agent-created issues are shared workspace-wide (DENE-698): 'private'
	// means "only its creator", and an agent is not somebody a list is shown
	// to. The service stamps this on create; a fixture that writes the row
	// directly has to say it.
	issueID := dbfx.Issue(t, "agent chain budget", testutil.Cols{
		"creator_type": "agent",
		"creator_id":   agentA,
		"visibility":   "workspace",
	})

	var sourceTaskID string
	for i := 0; i < 6; i++ {
		agentID := agentA
		if i%2 == 1 {
			agentID = agentB
		}
		taskID := seedDelegatedTask(t, agentID, issueID)
		if i == 0 {
			sourceTaskID = taskID
		}
	}
	triggerCommentID := dbfx.Comment(t, issueID, "@agent please continue", testutil.Cols{
		"author_type":    "agent",
		"author_id":      agentB,
		"source_task_id": sourceTaskID,
	})

	issue := issueForGuardTest(t, issueID)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := testHandler.TaskService.EnqueueTaskForMention(
			context.Background(), issue, util.MustParseUUID(agentA), util.MustParseUUID(triggerCommentID), service.OriginNamed,
		)
		if !errors.Is(err, service.ErrAgentChainBudgetExceeded) {
			t.Fatalf("delegated attempt %d error = %v, want ErrAgentChainBudgetExceeded", attempt+1, err)
		}
	}
	if got := countBudgetNotices(t, issueID); got != 1 {
		t.Fatalf("budget notices = %d, want exactly one", got)
	}
	if got := budgetNoticeParentID(t, issueID); got != triggerCommentID {
		t.Fatalf("budget notice parent_id = %q, want triggering comment %s", got, triggerCommentID)
	}

	// Route an actual member comment through the handler so the same code that
	// serves users clears both the halt latch and the budget notice marker.
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "human reset"})
	req = withURLParam(req, "id", issueID)
	resp := httptest.NewRecorder()
	testHandler.CreateComment(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("human reset comment: status = %d, body = %s", resp.Code, resp.Body.String())
	}

	issue = issueForGuardTest(t, issueID)
	if _, err := testHandler.TaskService.EnqueueTaskForMention(
		context.Background(), issue, util.MustParseUUID(agentA), util.MustParseUUID(triggerCommentID), service.OriginNamed,
	); err != nil {
		t.Fatalf("delegated trigger after human comment: %v", err)
	}
}

// TestIssueAgentChainBudgetDefaultAndUnlimited pins the two values a workspace
// that never opened the setting relies on: the unconfigured default admits a
// relay well past the old limit of six (DENE-691), and zero removes the cap.
func TestIssueAgentChainBudgetDefaultAndUnlimited(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for name, tc := range map[string]struct {
		budget    string // raw JSON; "" leaves the setting absent
		seeded    int
		wantBlock bool
	}{
		"default admits the 7th run":    {budget: "", seeded: 6},
		"default blocks at 30":          {budget: "", seeded: 30, wantBlock: true},
		"zero is unlimited":             {budget: "0", seeded: 40},
		"malformed falls back to 30":    {budget: `"lots"`, seeded: 30, wantBlock: true},
		"configured value is respected": {budget: "8", seeded: 8, wantBlock: true},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.budget != "" {
				setAgentChainBudget(t, tc.budget)
			} else {
				clearHandlerTestAgentChainBudget(t)
			}
			agentA := createHandlerTestAgent(t, "Guard Default Agent A", []byte("[]"))
			agentB := createHandlerTestAgent(t, "Guard Default Agent B", []byte("[]"))
			issueID := dbfx.Issue(t, "agent chain default budget", testutil.Cols{
				"creator_type": "agent",
				"creator_id":   agentA,
			})
			var sourceTaskID string
			for i := 0; i < tc.seeded; i++ {
				agentID := agentA
				if i%2 == 1 {
					agentID = agentB
				}
				taskID := seedDelegatedTask(t, agentID, issueID)
				if i == 0 {
					sourceTaskID = taskID
				}
			}
			triggerCommentID := dbfx.Comment(t, issueID, "@agent please continue", testutil.Cols{
				"author_type":    "agent",
				"author_id":      agentB,
				"source_task_id": sourceTaskID,
			})
			_, err := testHandler.TaskService.EnqueueTaskForMention(
				context.Background(), issueForGuardTest(t, issueID), util.MustParseUUID(agentA), util.MustParseUUID(triggerCommentID), service.OriginNamed,
			)
			if blocked := errors.Is(err, service.ErrAgentChainBudgetExceeded); blocked != tc.wantBlock {
				t.Fatalf("after %d delegated runs: err = %v, want blocked = %v", tc.seeded, err, tc.wantBlock)
			}
		})
	}
}

// TestHaltIssueCancelsActiveRunsAndPreservesHumanTriggers verifies that the
// issue-level halt cancels every active queue state, blocks agent-originated
// work, and still permits a member's explicit trigger.
func TestHaltIssueCancelsActiveRunsAndPreservesHumanTriggers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "Guard Halt Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "halt issue", testutil.Cols{
		"creator_type":  "agent",
		"creator_id":    agentID,
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"visibility":    "workspace",
	})
	active := []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"}
	taskIDs := make(map[string]string, len(active))
	// One agent per status: idx_one_pending_task_per_issue_agent_thread is
	// unique over (issue, agent, thread) for queued/dispatched, so seeding all
	// five states onto a single agent collides before the halt ever runs.
	for _, status := range active {
		statusAgent := createHandlerTestAgent(t, "Guard Halt Agent "+status, []byte("[]"))
		taskIDs[status] = insertIssueTaskWithStatus(t, statusAgent, issueID, status)
	}

	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/halt", nil)
	req = withURLParam(req, "id", issueID)
	resp := httptest.NewRecorder()
	testHandler.HaltIssue(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("halt issue: status = %d, body = %s", resp.Code, resp.Body.String())
	}
	for status, taskID := range taskIDs {
		if got := taskStatus(t, taskID); got != "cancelled" {
			t.Errorf("%s task status = %q, want cancelled", status, got)
		}
	}

	issue := issueForGuardTest(t, issueID)
	if _, err := testHandler.TaskService.EnqueueTaskForIssue(context.Background(), issue); !errors.Is(err, service.ErrAgentChainBudgetExceeded) {
		t.Fatalf("agent trigger during halt error = %v, want ErrAgentChainBudgetExceeded", err)
	}
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(context.Background(), issue, util.MustParseUUID(testUserID)); err != nil {
		t.Fatalf("human trigger during halt: %v", err)
	}
}

// Resume clears an explicit halt, but it is not a budget reset. Once the
// chain count is over the threshold, resuming must stay blocked and must not
// emit a duplicate one-shot notice; only a new human comment resets the count.
func TestResumeDoesNotResetBudgetOrDuplicateNotice(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setHandlerTestAgentChainBudget(t, 6)
	agentID := createHandlerTestAgent(t, "Guard Resume Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "resume budget", testutil.Cols{
		"creator_type": "agent",
		"creator_id":   agentID,
		"visibility":   "workspace",
	})
	sourceTaskID := seedDelegatedTasks(t, agentID, issueID, 6)
	triggerCommentID := dbfx.Comment(t, issueID, "resume trigger", testutil.Cols{
		"author_type":    "agent",
		"author_id":      agentID,
		"source_task_id": sourceTaskID,
	})
	issue := issueForGuardTest(t, issueID)
	_, err := testHandler.TaskService.EnqueueTaskForMention(context.Background(), issue, util.MustParseUUID(agentID), util.MustParseUUID(triggerCommentID), service.OriginNamed)
	if !errors.Is(err, service.ErrAgentChainBudgetExceeded) {
		t.Fatalf("initial delegated attempt error = %v, want budget exceeded", err)
	}

	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/resume", nil)
	req = withURLParam(req, "id", issueID)
	resp := httptest.NewRecorder()
	testHandler.ResumeIssue(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("resume issue: status = %d, body = %s", resp.Code, resp.Body.String())
	}

	issue = issueForGuardTest(t, issueID)
	_, err = testHandler.TaskService.EnqueueTaskForMention(context.Background(), issue, util.MustParseUUID(agentID), util.MustParseUUID(triggerCommentID), service.OriginNamed)
	if !errors.Is(err, service.ErrAgentChainBudgetExceeded) {
		t.Fatalf("delegated attempt after resume error = %v, want budget exceeded", err)
	}
	if got := countBudgetNotices(t, issueID); got != 1 {
		t.Fatalf("budget notices after resume = %d, want exactly one", got)
	}
}

// TestIssueAgentChainBudgetDefaultAllowsMoreThanSix is the DENE-693 acceptance
// case: the product default is 30, so a 7th agent-originated @-relay must still
// enqueue instead of stopping at the old hardcoded 6.
func TestIssueAgentChainBudgetDefaultAllowsMoreThanSix(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	clearHandlerTestAgentChainBudget(t)
	agentID := createHandlerTestAgent(t, "Guard Default Budget Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "default chain budget", testutil.Cols{
		"creator_type": "agent",
		"creator_id":   agentID,
		"visibility":   "workspace",
	})
	sourceTaskID := seedDelegatedTasks(t, agentID, issueID, 6)
	triggerCommentID := dbfx.Comment(t, issueID, "continue past six", testutil.Cols{
		"author_type":    "agent",
		"author_id":      agentID,
		"source_task_id": sourceTaskID,
	})
	issue := issueForGuardTest(t, issueID)
	if _, err := testHandler.TaskService.EnqueueTaskForMention(
		context.Background(), issue, util.MustParseUUID(agentID), util.MustParseUUID(triggerCommentID), service.OriginNamed,
	); err != nil {
		t.Fatalf("7th delegated enqueue with default budget: %v", err)
	}
	if got := countBudgetNotices(t, issueID); got != 0 {
		t.Fatalf("budget notices = %d, want none under the default cap", got)
	}
}

func TestGetWorkspaceAgentChainBudgetDefaultAndOverride(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slug := fmt.Sprintf("cb-%d", time.Now().UnixNano())
	var wsID string
	dbfx.QueryRow(t, `
		INSERT INTO workspace (name, slug)
		VALUES ('chain budget default', $1)
		RETURNING id
	`, slug).Scan(&wsID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	got, err := testHandler.Queries.GetWorkspaceAgentChainBudget(context.Background(), util.MustParseUUID(wsID))
	if err != nil {
		t.Fatalf("default budget lookup: %v", err)
	}
	if got != service.DefaultAgentChainBudget {
		t.Fatalf("default budget = %d, want %d", got, service.DefaultAgentChainBudget)
	}

	dbfx.Exec(t, `UPDATE workspace SET settings = '{"agent_chain_budget": 2}'::jsonb WHERE id = $1`, wsID)
	got, err = testHandler.Queries.GetWorkspaceAgentChainBudget(context.Background(), util.MustParseUUID(wsID))
	if err != nil {
		t.Fatalf("override budget lookup: %v", err)
	}
	if got != 2 {
		t.Fatalf("override budget = %d, want 2", got)
	}

	dbfx.Exec(t, `UPDATE workspace SET settings = '{"agent_chain_budget": 0}'::jsonb WHERE id = $1`, wsID)
	got, err = testHandler.Queries.GetWorkspaceAgentChainBudget(context.Background(), util.MustParseUUID(wsID))
	if err != nil {
		t.Fatalf("unlimited budget lookup: %v", err)
	}
	if got != 0 {
		t.Fatalf("unlimited budget = %d, want 0", got)
	}
}
