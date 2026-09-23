package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

type quotaWorld struct {
	pool        *pgxpool.Pool
	workspaceID string
	userID      string
	runtimeID   string
	parentID    string
	failedID    string
	sameID      string
	mediumID    string
	siblingID   string
	issueID     string
	commentID   string
	taskID      string
	reviewerID  string
}

func seedQuotaWorld(t *testing.T, failureReason, errorText string, ownModel bool) quotaWorld {
	t.Helper()
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	w := quotaWorld{pool: pool}

	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"quota-user", fmt.Sprintf("quota-%d@multica.test", suffix)).Scan(&w.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_quota_relay WHERE workspace_id = $1`, w.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_quota_breaker WHERE workspace_id = $1`, w.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, w.workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, w.userID)
	})

	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"quota ws", fmt.Sprintf("quota-%d", suffix)).Scan(&w.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		w.workspaceID, w.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, $2, 'local', 'codex', 'online', '', '{}'::jsonb, $3, now())
		RETURNING id`, w.workspaceID, fmt.Sprintf("quota-rt-%d", suffix), w.userID).Scan(&w.runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}

	insertAgent := func(name, model, tier string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility,
				max_concurrent_tasks, owner_id, instructions, custom_env, custom_args, model, routing_tier)
			VALUES ($1, $2, 'local', '{}'::jsonb, $3, 'workspace', 1, $4, '', '{}'::jsonb, '[]'::jsonb, $5, NULLIF($6, ''))
			RETURNING id`, w.workspaceID, name, w.runtimeID, w.userID, model, tier).Scan(&id); err != nil {
			t.Fatalf("seed agent %s: %v", name, err)
		}
		return id
	}
	w.parentID = insertAgent(fmt.Sprintf("quota-base-%d", suffix), "shared-model", "")
	w.failedID = insertAgent(fmt.Sprintf("quota-spent-%d", suffix), "gpt-special", "strong")
	w.sameID = insertAgent(fmt.Sprintf("quota-same-%d", suffix), "other-model", "strong")
	w.mediumID = insertAgent(fmt.Sprintf("quota-down-%d", suffix), "down-model", "medium")
	w.siblingID = insertAgent(fmt.Sprintf("quota-sibling-%d", suffix), "gpt-special", "")
	w.reviewerID = w.mediumID

	inherited := !ownModel
	if _, err := pool.Exec(ctx, `
		UPDATE agent
		SET parent_agent_id = $2, runtime_inherited = $3
		WHERE id = $1`, w.failedID, w.parentID, inherited); err != nil {
		t.Fatalf("bind specialisation: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id,
			priority, status, reviewer_type, reviewer_id, acceptance_criteria)
		VALUES ($1, '额度接力', 'member', $2, 'agent', $3, 'high', 'in_progress', 'agent', $4, '["定向测试通过"]'::jsonb)
		RETURNING id`, w.workspaceID, w.userID, w.failedID, w.reviewerID).Scan(&w.issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, '请继续原来的工作线程')
		RETURNING id`, w.issueID, w.workspaceID, w.userID).Scan(&w.commentID); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, failure_reason, error,
			trigger_comment_id, originator_user_id, accountable_user_id
		)
		VALUES ($1, $2, $3, 'failed', 0, $4, $5, $6, $7, $7)
		RETURNING id`, w.failedID, w.runtimeID, w.issueID, failureReason, errorText, w.commentID, w.userID).Scan(&w.taskID); err != nil {
		t.Fatalf("seed failed task: %v", err)
	}
	return w
}

func (w quotaWorld) service() *TaskService {
	return &TaskService{Queries: db.New(w.pool), TxStarter: w.pool, Bus: events.New()}
}

func (w quotaWorld) task(t *testing.T) db.AgentTaskQueue {
	t.Helper()
	task, err := w.service().Queries.GetAgentTask(context.Background(), util.MustParseUUID(w.taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	return task
}

func TestQuotaRelayHandsOffSameTierWithoutTouchingSharedModel(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "model quota exceeded for gpt-special", true)
	ctx := context.Background()
	svc := w.service()

	hold, err := svc.RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("relay hold=%v err=%v", hold, err)
	}
	// A second failure report must not dispatch another run.
	hold, err = svc.RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("second relay hold=%v err=%v", hold, err)
	}

	var scope, modelKey, failedModel, parentModel, siblingModel string
	var failedEnabled, siblingEnabled bool
	if err := w.pool.QueryRow(ctx, `
		SELECT scope, model_key FROM agent_quota_breaker
		WHERE agent_id = $1 AND recovered_at IS NULL`, w.failedID).Scan(&scope, &modelKey); err != nil {
		t.Fatalf("breaker: %v", err)
	}
	if scope != "model" || modelKey != "gpt-special" {
		t.Fatalf("breaker scope/model = %s/%s", scope, modelKey)
	}
	if err := w.pool.QueryRow(ctx, `SELECT model, work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&failedModel, &failedEnabled); err != nil {
		t.Fatalf("failed agent: %v", err)
	}
	if failedModel != "gpt-special" || failedEnabled {
		t.Fatalf("failed agent model=%s enabled=%v, want gpt-special disabled", failedModel, failedEnabled)
	}
	if err := w.pool.QueryRow(ctx, `SELECT model FROM agent WHERE id = $1`, w.parentID).Scan(&parentModel); err != nil {
		t.Fatalf("parent model: %v", err)
	}
	if parentModel != "shared-model" {
		t.Fatalf("parent model = %s, shared config was rewritten", parentModel)
	}
	if err := w.pool.QueryRow(ctx, `SELECT model, work_enabled FROM agent WHERE id = $1`, w.siblingID).Scan(&siblingModel, &siblingEnabled); err != nil {
		t.Fatalf("sibling: %v", err)
	}
	if siblingModel != "gpt-special" || !siblingEnabled {
		t.Fatalf("sibling model=%s enabled=%v, another seat sharing the model string was changed", siblingModel, siblingEnabled)
	}

	var assignee, status string
	if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text, status FROM issue WHERE id = $1`, w.issueID).Scan(&assignee, &status); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if assignee != w.sameID || status != "in_progress" {
		t.Fatalf("issue assignee/status = %s/%s, want same-tier seat still in progress", assignee, status)
	}

	var queued int
	var handoff, trigger, thread string
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(handoff_note), ''), COALESCE(max(trigger_comment_id::text), ''),
		       COALESCE(max(comment_thread_id::text), '')
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, w.issueID, w.sameID).Scan(&queued, &handoff, &trigger, &thread); err != nil {
		t.Fatalf("replacement task: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued replacement tasks = %d, want 1", queued)
	}
	if trigger != w.commentID || thread != w.commentID {
		t.Fatalf("replacement thread trigger=%s thread=%s, want comment %s", trigger, thread, w.commentID)
	}
	for _, want := range []string{"from_task=" + w.taskID, "thread=" + w.commentID, "reviewer_id=" + w.reviewerID, "定向测试通过", "继续", "step=same", "scope=model"} {
		if !strings.Contains(handoff, want) {
			t.Errorf("handoff missing %q\n%s", want, handoff)
		}
	}

	var relays int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_quota_relay WHERE source_task_id = $1 AND outcome = 'relayed'`, w.taskID).Scan(&relays); err != nil {
		t.Fatalf("relay rows: %v", err)
	}
	if relays != 1 {
		t.Fatalf("relay rows = %d, want 1", relays)
	}

	_, err = svc.Queries.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
		AgentID:          util.MustParseUUID(w.failedID),
		RuntimeID:        util.MustParseUUID(w.runtimeID),
		PrepareLeaseSecs: 60,
		RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
	})
	if !errorsIsNoRows(err) {
		t.Fatalf("claim of broken seat = %v, want no row so the spent quota is not consumed again", err)
	}
}

func TestQuotaRelayDropsOneTierWhenSameTierIsUnavailable(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET routing_tier = NULL WHERE id = $1`, w.sameID); err != nil {
		t.Fatalf("clear same tier: %v", err)
	}
	if _, err := w.service().RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	var assignee, handoff string
	if err := w.pool.QueryRow(ctx, `
		SELECT i.assignee_id::text, COALESCE(t.handoff_note, '')
		FROM issue i
		LEFT JOIN agent_task_queue t ON t.issue_id = i.id AND t.agent_id = i.assignee_id AND t.status = 'queued'
		WHERE i.id = $1`, w.issueID).Scan(&assignee, &handoff); err != nil {
		t.Fatalf("read handoff: %v", err)
	}
	if assignee != w.mediumID {
		t.Fatalf("assignee = %s, want the one-tier-down seat %s", assignee, w.mediumID)
	}
	if !strings.Contains(handoff, "step=down") || !strings.Contains(handoff, "scope=agent") {
		t.Fatalf("handoff = %s", handoff)
	}
	var scope, modelKey string
	if err := w.pool.QueryRow(ctx, `SELECT scope, model_key FROM agent_quota_breaker WHERE agent_id = $1`, w.failedID).Scan(&scope, &modelKey); err != nil {
		t.Fatalf("breaker: %v", err)
	}
	if scope != "agent" || modelKey != "" {
		t.Fatalf("weekly breaker = %s/%s", scope, modelKey)
	}
}

func TestInheritedModelQuotaDoesNotBreakTheSharedModel(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "model quota exceeded for gpt-special", false)
	ctx := context.Background()
	if _, err := w.service().RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	var scope, modelKey, parentModel string
	var parentEnabled bool
	if err := w.pool.QueryRow(ctx, `SELECT scope, model_key FROM agent_quota_breaker WHERE agent_id = $1`, w.failedID).Scan(&scope, &modelKey); err != nil {
		t.Fatalf("breaker: %v", err)
	}
	if scope != "agent" || modelKey != "" {
		t.Fatalf("inherited model failure breaker = %s/%s, want agent scope only", scope, modelKey)
	}
	if err := w.pool.QueryRow(ctx, `SELECT model, work_enabled FROM agent WHERE id = $1`, w.parentID).Scan(&parentModel, &parentEnabled); err != nil {
		t.Fatalf("parent: %v", err)
	}
	if parentModel != "shared-model" || !parentEnabled {
		t.Fatalf("parent model=%s enabled=%v", parentModel, parentEnabled)
	}
}

func TestQuotaRelayWaitsAndNotifiesWhenNoSeatExists(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET routing_tier = NULL WHERE id = $1 OR id = $2`, w.sameID, w.mediumID); err != nil {
		t.Fatalf("clear ladder: %v", err)
	}
	hold, err := w.service().RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("relay hold=%v err=%v", hold, err)
	}
	var status, outcome string
	var queued, inbox int
	if err := w.pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, w.issueID).Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	if status != "blocked" {
		t.Fatalf("status = %s, want blocked", status)
	}
	if err := w.pool.QueryRow(ctx, `SELECT outcome FROM agent_quota_relay WHERE source_task_id = $1`, w.taskID).Scan(&outcome); err != nil {
		t.Fatalf("outcome: %v", err)
	}
	if outcome != "waiting" {
		t.Fatalf("outcome = %s", outcome)
	}
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND status = 'queued'`, w.issueID).Scan(&queued); err != nil {
		t.Fatalf("queued: %v", err)
	}
	if queued != 0 {
		t.Fatalf("queued tasks = %d, want 0", queued)
	}
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND recipient_type = 'member' AND recipient_id = $2
		  AND issue_id = $3 AND type = 'quota_relay_waiting' AND severity = 'action_required'`,
		w.workspaceID, w.userID, w.issueID).Scan(&inbox); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if inbox != 1 {
		t.Fatalf("inbox items = %d, want 1", inbox)
	}
	hold, err = w.service().RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("second wait hold=%v err=%v", hold, err)
	}
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND type = 'quota_relay_waiting'`, w.issueID).Scan(&inbox); err != nil {
		t.Fatalf("inbox recount: %v", err)
	}
	if inbox != 1 {
		t.Fatalf("second report created inbox count %d", inbox)
	}
}

func TestCapacityFailureDoesNotBreakOrRelay(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "API Error: 429 Too Many Requests", true)
	ctx := context.Background()
	hold, err := w.service().RelayQuotaFailure(ctx, w.task(t))
	if err != nil || hold {
		t.Fatalf("capacity relay hold=%v err=%v, want no relay", hold, err)
	}
	var breakers, relays int
	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_quota_breaker WHERE agent_id = $1`, w.failedID).Scan(&breakers); err != nil {
		t.Fatalf("breakers: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_quota_relay WHERE source_task_id = $1`, w.taskID).Scan(&relays); err != nil {
		t.Fatalf("relays: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if breakers != 0 || relays != 0 || !enabled {
		t.Fatalf("breakers=%d relays=%d enabled=%v, capacity must not trip the breaker", breakers, relays, enabled)
	}
}

func TestQuotaRecoveryReenablesOnlyAnUntouchedSeat(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached. resets in 1h", true)
	ctx := context.Background()
	svc := w.service()
	if _, err := svc.RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET description = 'human edit', updated_at = now() WHERE id = $1`, w.siblingID); err != nil {
		t.Fatalf("touch sibling: %v", err)
	}
	// UpdateAgent bumps updated_at. A later write must keep recovery from
	// turning the seat back on; the raw column change is that signal.
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET description = 'human edit', updated_at = now() WHERE id = $1`, w.failedID); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE agent_quota_breaker SET recover_at = now() - interval '1 minute' WHERE agent_id = $1`, w.failedID); err != nil {
		t.Fatalf("expire breaker: %v", err)
	}
	released, err := svc.RecoverExpiredQuotaBreakers(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if released != 0 {
		t.Fatalf("released = %d, want 0 after a later edit", released)
	}
	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("failed enabled: %v", err)
	}
	if enabled {
		t.Fatal("recovery re-enabled a seat that was edited after the breaker")
	}

	// Put the suppress timestamp back in line with the agent, as it is when
	// nobody else has written the row, and recover again. The breaker is
	// already closed, so open a fresh one through a second task.
	var second string
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, failure_reason, error,
			originator_user_id, accountable_user_id
		)
		VALUES ($1, $2, $3, 'failed', 0, $4, 'Weekly usage limit reached', $5, $5)
		RETURNING id`, w.failedID, w.runtimeID, w.issueID, string(taskfailure.ReasonAgentProviderQuotaLimit), w.userID).Scan(&second); err != nil {
		t.Fatalf("second task: %v", err)
	}
	// The issue was reassigned. Point it back so this failure is still the assignee's.
	if _, err := w.pool.Exec(ctx, `
		UPDATE issue SET assignee_type = 'agent', assignee_id = $1, status = 'in_progress' WHERE id = $2`, w.failedID, w.issueID); err != nil {
		t.Fatalf("reassign back: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET work_enabled = TRUE, description = '' WHERE id = $1`, w.failedID); err != nil {
		t.Fatalf("re-enable for second hit: %v", err)
	}
	secondTask, err := svc.Queries.GetAgentTask(ctx, util.MustParseUUID(second))
	if err != nil {
		t.Fatalf("load second: %v", err)
	}
	if _, err := svc.RelayQuotaFailure(ctx, secondTask); err != nil {
		t.Fatalf("second relay: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `
		UPDATE agent_quota_breaker
		SET recover_at = now() - interval '1 minute'
		WHERE agent_id = $1 AND recovered_at IS NULL`, w.failedID); err != nil {
		t.Fatalf("expire second breaker: %v", err)
	}
	released, err = svc.RecoverExpiredQuotaBreakers(ctx)
	if err != nil {
		t.Fatalf("second recover: %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("re-enabled: %v", err)
	}
	if !enabled {
		t.Fatal("untouched seat stayed disabled after the quota window")
	}
	var siblingEnabled bool
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.siblingID).Scan(&siblingEnabled); err != nil {
		t.Fatalf("sibling: %v", err)
	}
	if !siblingEnabled {
		t.Fatal("recovery changed a seat that was not broken")
	}
}

func TestHandleFailedTasksQuotaRelayDoesNotRetry(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	ctx := context.Background()
	svc := w.service()
	if retried := svc.HandleFailedTasks(ctx, []db.AgentTaskQueue{w.task(t)}); retried != 0 {
		t.Fatalf("retried = %d, want 0", retried)
	}
	var retries int
	var status string
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, w.taskID).Scan(&retries); err != nil {
		t.Fatalf("retries: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, w.issueID).Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	if retries != 0 || status != "in_progress" {
		t.Fatalf("retries=%d status=%s, want no retry and the issue kept in progress by the relay", retries, status)
	}
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
