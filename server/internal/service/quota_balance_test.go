package service

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// DENE-870: a 402 turns the seat off with a reason that never times out,
// tells the owner once, and moves the seat's todo and started work to
// another house, never onto a ticket's own reviewer.
func TestBalanceExhaustionMovesWorkCrossHouseAndAlertsOnce(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "API Error: 402 Payment Required: insufficient balance", true)
	ctx := context.Background()
	rename := func(id, name, tier string) {
		t.Helper()
		if _, err := w.pool.Exec(ctx, `UPDATE agent SET name = $2, routing_tier = $3 WHERE id = $1`, id, name, tier); err != nil {
			t.Fatalf("rename %s: %v", name, err)
		}
	}
	rename(w.failedID, "孙悟天", "strong")   // Grok, account spent
	rename(w.parentID, "孙悟天游戏", "strong") // same house: must not be picked
	rename(w.sameID, "特兰克斯", "strong")    // GPT
	rename(w.mediumID, "孙悟空", "strong")   // Claude, reviewer of the source ticket
	rename(w.siblingID, "贝吉塔", "medium")
	if _, err := w.pool.Exec(ctx, `UPDATE issue SET reviewer_id = $2 WHERE id = $1`, w.issueID, w.mediumID); err != nil {
		t.Fatalf("source reviewer: %v", err)
	}
	// The other houses run their own CLIs, so their own runtime. Only the
	// Grok family and the seat bound to the same Grok account stay behind.
	otherRuntime := seedQuotaRuntime(t, w, "other-house")
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET runtime_id = $4 WHERE id IN ($1, $2, $3)`,
		w.sameID, w.mediumID, w.siblingID, otherRuntime); err != nil {
		t.Fatalf("move other houses: %v", err)
	}
	sameAccountID := seedQuotaAgent(t, w, "jump-broker维护", w.runtimeID, `{}`)
	otherAccountID := seedQuotaAgent(t, w, "孙悟天二号", w.runtimeID, `{"GROK_HOME":"/accounts/2"}`)

	insertIssue := func(title, status string, number int, reviewer string) string {
		t.Helper()
		var id string
		if err := w.pool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, priority, status, number,
				reviewer_type, reviewer_id)
			VALUES ($1, $2, 'member', $3, 'agent', $4, 'high', $5, $6,
				CASE WHEN $7 = '' THEN NULL ELSE 'agent' END, NULLIF($7, '')::uuid)
			RETURNING id`, w.workspaceID, title, w.userID, w.failedID, status, number, reviewer).Scan(&id); err != nil {
			t.Fatalf("issue %s: %v", title, err)
		}
		return id
	}
	// Started earlier, nothing queued: a capacity miss would leave it, a
	// spent account must not.
	startedID := insertIssue("做到一半", "in_progress", 9101, "")
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, completed_at)
		VALUES ($1, $2, $3, 'completed', 0, now() - interval '3 hours')`, w.failedID, w.runtimeID, startedID); err != nil {
		t.Fatalf("started task: %v", err)
	}
	// Its reviewer is the GPT seat, so it must go to Claude.
	todoID := insertIssue("还没开跑", "todo", 9102, w.sameID)
	// The base role's own ticket has to leave the spent account too.
	baseTicketID := insertIssue("基础角色手上的", "in_progress", 9104, "")
	if _, err := w.pool.Exec(ctx, `UPDATE issue SET assignee_id = $2 WHERE id = $1`, baseTicketID, w.parentID); err != nil {
		t.Fatalf("base ticket: %v", err)
	}

	svc := w.service()
	hold, err := svc.RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("relay hold=%v err=%v", hold, err)
	}

	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("seat: %v", err)
	}
	if enabled {
		t.Fatal("a seat whose account is spent must stop taking work")
	}
	// Kun's rule: shut the whole account down before moving the work. The
	// base role, its specialisation and the seat on the same account are
	// all off; a seat bound to another account keeps working.
	for _, id := range []string{w.parentID, sameAccountID} {
		var on bool
		var open int
		if err := w.pool.QueryRow(ctx, `
			SELECT a.work_enabled, (SELECT count(*) FROM agent_quota_breaker b
				WHERE b.agent_id = a.id AND b.recovered_at IS NULL AND b.reason = 'balance_exhausted')
			FROM agent a WHERE a.id = $1`, id).Scan(&on, &open); err != nil {
			t.Fatalf("account seat %s: %v", id, err)
		}
		if on || open != 1 {
			t.Fatalf("seat %s on the spent account enabled=%v open breakers=%d", id, on, open)
		}
	}
	var otherOn bool
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, otherAccountID).Scan(&otherOn); err != nil {
		t.Fatalf("other account seat: %v", err)
	}
	if !otherOn {
		t.Fatal("a seat bound to another account was shut down")
	}
	var stranded int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM issue WHERE assignee_id IN ($1, $2, $3) AND status IN ('todo', 'in_progress', 'blocked')`,
		w.failedID, w.parentID, sameAccountID).Scan(&stranded); err != nil {
		t.Fatalf("stranded: %v", err)
	}
	if stranded != 0 {
		t.Fatalf("%d open tickets still sit on the spent account", stranded)
	}

	var reason, condition string
	var farOff bool
	if err := w.pool.QueryRow(ctx, `
		SELECT reason, recover_condition, recover_at > now() + interval '50 years'
		FROM agent_quota_breaker WHERE agent_id = $1 AND recovered_at IS NULL`, w.failedID).Scan(&reason, &condition, &farOff); err != nil {
		t.Fatalf("breaker: %v", err)
	}
	if reason != "balance_exhausted" || !farOff || !strings.Contains(condition, "top up") {
		t.Fatalf("breaker reason=%s condition=%s farOff=%v", reason, condition, farOff)
	}

	assigneeOf := func(id string) string {
		t.Helper()
		var a string
		if err := w.pool.QueryRow(ctx, `SELECT assignee_id::text FROM issue WHERE id = $1`, id).Scan(&a); err != nil {
			t.Fatalf("assignee: %v", err)
		}
		return a
	}
	if got := assigneeOf(w.issueID); got != w.sameID {
		t.Fatalf("source ticket went to %s, want 特兰克斯 %s (its reviewer is 孙悟空)", got, w.sameID)
	}
	if got := assigneeOf(todoID); got != w.mediumID {
		t.Fatalf("todo ticket went to %s, want 孙悟空 %s (its reviewer is 特兰克斯)", got, w.mediumID)
	}
	if got := assigneeOf(startedID); got == w.failedID || got == w.parentID {
		t.Fatalf("started ticket stayed in the Grok house: %s", got)
	}
	for _, id := range []string{todoID, startedID} {
		var comment string
		if err := w.pool.QueryRow(ctx, `
			SELECT content FROM comment WHERE issue_id = $1 AND routing_kind LIKE 'quota_relay:%'`, id).Scan(&comment); err != nil {
			t.Fatalf("transfer comment: %v", err)
		}
		if !strings.Contains(comment, "孙悟天") || !strings.Contains(comment, "余额") {
			t.Fatalf("transfer comment = %s", comment)
		}
		var queued int
		if err := w.pool.QueryRow(ctx, `
			SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id <> $2 AND status = 'queued'`, id, w.failedID).Scan(&queued); err != nil {
			t.Fatalf("queued: %v", err)
		}
		if queued != 1 {
			t.Fatalf("ticket %s queued %d runs for the new seat, want 1", id, queued)
		}
	}

	countAlerts := func() int {
		t.Helper()
		var n int
		if err := w.pool.QueryRow(ctx, `
			SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = 'seat_balance_exhausted' AND recipient_id = $2`,
			w.workspaceID, w.userID).Scan(&n); err != nil {
			t.Fatalf("alerts: %v", err)
		}
		return n
	}
	if n := countAlerts(); n != 1 {
		t.Fatalf("owner alerts = %d, want 1", n)
	}
	var alertBody string
	if err := w.pool.QueryRow(ctx, `
		SELECT body FROM inbox_item WHERE workspace_id = $1 AND type = 'seat_balance_exhausted'`, w.workspaceID).Scan(&alertBody); err != nil {
		t.Fatalf("alert body: %v", err)
	}
	if !strings.Contains(alertBody, "孙悟天游戏") || !strings.Contains(alertBody, "jump-broker维护") {
		t.Fatalf("owner alert must name the seats that went down with it:\n%s", alertBody)
	}

	// A run already on the wire fails on the same empty account: no second
	// reminder.
	lateID := insertIssue("在跑的", "in_progress", 9103, "")
	var lateTask string
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, failure_reason, error)
		VALUES ($1, $2, $3, 'failed', 0, $4, '402 Payment Required')
		RETURNING id`, w.failedID, w.runtimeID, lateID, string(taskfailure.ReasonAgentProviderQuotaLimit)).Scan(&lateTask); err != nil {
		t.Fatalf("late task: %v", err)
	}
	late, err := svc.Queries.GetAgentTask(ctx, mustUUID(t, lateTask))
	if err != nil {
		t.Fatalf("load late task: %v", err)
	}
	if _, err := svc.RelayQuotaFailure(ctx, late); err != nil {
		t.Fatalf("late relay: %v", err)
	}
	if n := countAlerts(); n != 1 {
		t.Fatalf("owner alerts after a second 402 = %d, want still 1", n)
	}
	if got := assigneeOf(lateID); got == w.failedID {
		t.Fatal("the late ticket stayed on the spent seat")
	}

	// Re-enabling closes the breaker; transferred tickets stay put.
	if n, err := svc.Queries.CloseManualQuotaBreakers(ctx, mustUUID(t, w.failedID)); err != nil || n != 1 {
		t.Fatalf("close manual breaker n=%d err=%v", n, err)
	}
	if got := assigneeOf(todoID); got != w.mediumID {
		t.Fatalf("todo ticket pulled back to %s", got)
	}
}

// DENE-870: a weekly window on a base role is its inheriting
// specialisations' window too; a specialisation with its own profile, and a
// seat merely sharing the runtime, keep working.
func TestWeeklyLimitOnABaseRoleTakesItsInheritingSpecialisations(t *testing.T) {
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", true)
	ctx := context.Background()
	// Make the failing seat the base role and hang two specialisations off it.
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET parent_agent_id = NULL, runtime_inherited = FALSE WHERE id = $1`, w.failedID); err != nil {
		t.Fatalf("base role: %v", err)
	}
	inheritedID := seedQuotaAgent(t, w, "孙悟饭游戏", w.runtimeID, `{}`)
	ownID := seedQuotaAgent(t, w, "孙悟饭学术", w.runtimeID, `{}`)
	if _, err := w.pool.Exec(ctx, `
		UPDATE agent SET parent_agent_id = $2::uuid, runtime_inherited = (id = $1::uuid) WHERE id IN ($1::uuid, $3::uuid)`,
		inheritedID, w.failedID, ownID); err != nil {
		t.Fatalf("specialisations: %v", err)
	}
	if _, err := w.service().RelayQuotaFailure(ctx, w.task(t)); err != nil {
		t.Fatalf("relay: %v", err)
	}
	enabled := func(id string) bool {
		t.Helper()
		var on bool
		if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, id).Scan(&on); err != nil {
			t.Fatalf("agent %s: %v", id, err)
		}
		return on
	}
	if enabled(w.failedID) || enabled(inheritedID) {
		t.Fatal("the base role and its inheriting specialisation must both stop")
	}
	if !enabled(ownID) || !enabled(w.siblingID) {
		t.Fatal("a weekly window must not reach a specialisation with its own profile or an unrelated seat")
	}
}

func seedQuotaRuntime(t *testing.T, w quotaWorld, name string) string {
	t.Helper()
	var id string
	if err := w.pool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, $2, 'local', 'claude', 'online', '', '{}'::jsonb, $3, now())
		RETURNING id`, w.workspaceID, name, w.userID).Scan(&id); err != nil {
		t.Fatalf("seed runtime %s: %v", name, err)
	}
	return id
}

func seedQuotaAgent(t *testing.T, w quotaWorld, name, runtimeID, env string) string {
	t.Helper()
	var id string
	if err := w.pool.QueryRow(context.Background(), `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility,
			max_concurrent_tasks, owner_id, instructions, custom_env, custom_args, model)
		VALUES ($1, $2, 'local', '{}'::jsonb, $3, 'workspace', 1, $4, '', $5::jsonb, '[]'::jsonb, 'grok-4.7')
		RETURNING id`, w.workspaceID, name, runtimeID, w.userID, env).Scan(&id); err != nil {
		t.Fatalf("seed agent %s: %v", name, err)
	}
	return id
}
