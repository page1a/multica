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
	// A seat of its own: the relay never hands work to the ticket's
	// reviewer (DENE-870), so sharing a candidate seat would steer picks.
	w.reviewerID = insertAgent(fmt.Sprintf("quota-reviewer-%d", suffix), "review-model", "")

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

// ADR-0008: a mention_only seat keeps its tier but is never the relay's pick.
// The same-tier seat is still tagged strong; only its dispatch mode changes,
// and the relay must step past it exactly as if it were off the rung.
func TestQuotaRelaySkipsMentionOnlySeat(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET dispatch_mode = 'mention_only' WHERE id = $1`, w.sameID); err != nil {
		t.Fatalf("mark same-tier seat mention_only: %v", err)
	}
	if _, err := w.service().RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	var assignee string
	if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text FROM issue WHERE id = $1`, w.issueID).Scan(&assignee); err != nil {
		t.Fatalf("read assignee: %v", err)
	}
	if assignee == w.sameID {
		t.Fatal("quota relay handed the ticket to a mention_only seat")
	}
	if assignee != w.mediumID {
		t.Fatalf("assignee = %s, want the one-tier-down auto seat %s", assignee, w.mediumID)
	}
}

func TestQuotaSeatSelectableRefusesMentionOnly(t *testing.T) {
	agent := db.Agent{WorkEnabled: true, RuntimeID: util.MustParseUUID("00000000-0000-0000-0000-000000000001"), DispatchMode: "auto"}
	if !quotaSeatSelectable(agent, "weak", false) {
		t.Fatal("an auto seat on a rung must be selectable")
	}
	agent.DispatchMode = "mention_only"
	if quotaSeatSelectable(agent, "weak", false) {
		t.Fatal("a mention_only seat must not be selectable by the quota relay")
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

// DENE-1093: a full model never trips the breaker, even after the old
// three-attempt budget. The seat, its siblings, and its other unstarted
// issues all stay where they are.
func TestCapacityFailureNeverRelays(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "Selected model is at capacity. Please try a different model.", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE agent_task_queue SET attempt = 3, max_attempts = 3 WHERE id = $1`, w.taskID); err != nil {
		t.Fatalf("spend old budget: %v", err)
	}
	var idleID string
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, priority, status, number)
		VALUES ($1, '还没开跑', 'member', $2, 'agent', $3, 'high', 'todo', 9001)
		RETURNING id`, w.workspaceID, w.userID, w.failedID).Scan(&idleID); err != nil {
		t.Fatalf("idle issue: %v", err)
	}
	hold, err := w.service().RelayQuotaFailure(ctx, w.task(t))
	if err != nil || hold {
		t.Fatalf("capacity relay hold=%v err=%v, want no relay", hold, err)
	}
	var breakers, relays, disabled int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_quota_breaker WHERE workspace_id = $1`, w.workspaceID).Scan(&breakers); err != nil {
		t.Fatalf("breakers: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_quota_relay WHERE source_task_id = $1`, w.taskID).Scan(&relays); err != nil {
		t.Fatalf("relays: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND NOT work_enabled`, w.workspaceID).Scan(&disabled); err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if breakers != 0 || relays != 0 || disabled != 0 {
		t.Fatalf("breakers=%d relays=%d disabled seats=%d, capacity must leave every seat open", breakers, relays, disabled)
	}
	for _, id := range []string{w.issueID, idleID} {
		var assignee string
		if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text FROM issue WHERE id = $1`, id).Scan(&assignee); err != nil {
			t.Fatalf("assignee: %v", err)
		}
		if assignee != w.failedID {
			t.Fatalf("issue %s moved to %s, capacity must not reassign", id, assignee)
		}
	}
}

// The daemon fail path at the old ceiling: the issue stays with the same
// seat and gets another deferred, session-resuming run instead of a relay.
func TestFailTaskCapacityKeepsTheIssueOnItsSeat(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "Selected model is at capacity. Please try a different model.", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'running', attempt = 3, max_attempts = 3, failure_reason = NULL, error = NULL, session_id = 'sess-cap'
		WHERE id = $1`, w.taskID); err != nil {
		t.Fatalf("reopen task: %v", err)
	}
	if _, err := w.service().FailTask(ctx, util.MustParseUUID(w.taskID), "Selected model is at capacity. Please try a different model.", "sess-cap", "", "", "", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	var assignee string
	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text FROM issue WHERE id = $1`, w.issueID).Scan(&assignee); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("seat: %v", err)
	}
	if assignee != w.failedID || !enabled {
		t.Fatalf("assignee=%s enabled=%v, want the same open seat", assignee, enabled)
	}
	var status, session string
	var attempt int32
	if err := w.pool.QueryRow(ctx, `
		SELECT status, COALESCE(session_id, ''), attempt FROM agent_task_queue WHERE parent_task_id = $1`, w.taskID).Scan(&status, &session, &attempt); err != nil {
		t.Fatalf("retry child: %v", err)
	}
	if status != "deferred" || session != "sess-cap" || attempt != 4 {
		t.Fatalf("child status=%s session=%s attempt=%d, want a deferred resume as attempt 4", status, session, attempt)
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

func TestRepeatedBreakersDemoteUntilASuccess(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "Selected model is at capacity. Please try a different model.", true)
	ctx := context.Background()
	insert := func() {
		t.Helper()
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO agent_quota_breaker (
				workspace_id, agent_id, scope, model_key, reason, recover_at, recovered_at, suppressed_work
			) VALUES ($1, $2, 'agent', '', 'provider_capacity', now(), now(), false)`,
			w.workspaceID, w.failedID); err != nil {
			t.Fatalf("breaker: %v", err)
		}
	}
	insert()
	queries := w.service().Queries
	ids, err := queries.ListDemotedQuotaAgentIDs(ctx, util.MustParseUUID(w.workspaceID))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("one breaker demoted %d seats", len(ids))
	}
	insert()
	ids, err = queries.ListDemotedQuotaAgentIDs(ctx, util.MustParseUUID(w.workspaceID))
	if err != nil {
		t.Fatalf("list after second: %v", err)
	}
	if len(ids) != 1 || util.UUIDToString(ids[0]) != w.failedID {
		t.Fatalf("demoted = %v, want %s", ids, w.failedID)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, completed_at)
		VALUES ($1, $2, 'completed', 0, now() + interval '1 minute')`, w.failedID, w.runtimeID); err != nil {
		t.Fatalf("success: %v", err)
	}
	ids, err = queries.ListDemotedQuotaAgentIDs(ctx, util.MustParseUUID(w.workspaceID))
	if err != nil {
		t.Fatalf("list after success: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("success left %d seats demoted", len(ids))
	}
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// DENE-1093 (the DENE-1066 gap): while a seat was off, triggers on the
// issues it still owned were refused. When its breaker expires and the seat
// comes back, those issues get a run again — but only the ones that are
// genuinely stranded.
func TestQuotaRecoveryRequeuesStrandedIssues(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached. resets in 1h", true)
	ctx := context.Background()
	svc := w.service()
	if _, err := svc.RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil || enabled {
		t.Fatalf("weekly limit must close the seat first: enabled=%v err=%v", enabled, err)
	}

	number := 9100
	issue := func(title, status, metadata string) string {
		t.Helper()
		number++
		var id string
		if err := w.pool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, priority, status, metadata, number)
			VALUES ($1, $2, 'member', $3, 'agent', $4, 'high', $5, $6::jsonb, $7)
			RETURNING id`, w.workspaceID, title, w.userID, w.failedID, status, metadata, number).Scan(&id); err != nil {
			t.Fatalf("seed issue %s: %v", title, err)
		}
		return id
	}
	strandedTodo := issue("被卡住的待办", "todo", `{}`)
	strandedWorking := issue("被卡住的进行中", "in_progress", `{}`)
	halted := issue("已叫停", "in_progress", `{"agent_halted": true}`)
	done := issue("已完成", "done", `{}`)
	backlog := issue("还在规划", "backlog", `{}`)
	running := issue("已经有人在跑", "in_progress", `{}`)
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, originator_user_id, accountable_user_id)
		VALUES ($1, $2, $3, 'queued', 0, $4, $4)`, w.sameID, w.runtimeID, running, w.userID); err != nil {
		t.Fatalf("seed running: %v", err)
	}

	if _, err := w.pool.Exec(ctx, `
		UPDATE agent_quota_breaker SET recover_at = now() - interval '1 minute'
		WHERE agent_id = $1 AND recovered_at IS NULL`, w.failedID); err != nil {
		t.Fatalf("expire breaker: %v", err)
	}
	if released, err := svc.RecoverExpiredQuotaBreakers(ctx); err != nil || released != 1 {
		t.Fatalf("recover released=%d err=%v", released, err)
	}

	runsFor := func(issueID string) int {
		t.Helper()
		var n int
		if err := w.pool.QueryRow(ctx, `
			SELECT count(*) FROM agent_task_queue
			WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, w.failedID).Scan(&n); err != nil {
			t.Fatalf("count runs: %v", err)
		}
		return n
	}
	for _, id := range []string{strandedTodo, strandedWorking} {
		if got := runsFor(id); got != 1 {
			t.Fatalf("stranded issue %s has %d queued runs, want 1", id, got)
		}
	}
	for _, id := range []string{halted, done, backlog, running} {
		if got := runsFor(id); got != 0 {
			t.Fatalf("issue %s got %d runs; only stranded todo/in_progress issues requeue", id, got)
		}
	}

	// A second pass finds nothing left to do.
	if n := svc.RequeueStrandedIssues(ctx, util.MustParseUUID(w.failedID)); n != 0 {
		t.Fatalf("second requeue queued %d, want 0", n)
	}
}
