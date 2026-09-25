-- Quota circuit breaker and same-tier / one-tier-down relay (DENE-771).

-- name: GetQuotaRelayBySourceTask :one
SELECT * FROM agent_quota_relay
WHERE source_task_id = $1;

-- name: LockQuotaRelayBySourceTask :one
SELECT * FROM agent_quota_relay
WHERE source_task_id = $1
FOR UPDATE;

-- name: InsertQuotaRelay :one
INSERT INTO agent_quota_relay (
    workspace_id, source_task_id, issue_id, from_agent_id, to_agent_id,
    scope, model_key, outcome, tier_from, tier_to, wait_reason,
    handoff_note, audit_comment, trigger_comment_id
) VALUES (
    $1, $2, sqlc.narg('issue_id'), $3, sqlc.narg('to_agent_id'),
    $4, $5, $6, $7, $8, $9,
    $10, $11, sqlc.narg('trigger_comment_id')
)
ON CONFLICT (source_task_id) DO NOTHING
RETURNING *;

-- name: MarkQuotaRelayRelayed :execrows
UPDATE agent_quota_relay
SET outcome = 'relayed', updated_at = now()
WHERE source_task_id = $1 AND outcome = 'pending';

-- name: AbandonQuotaRelay :execrows
UPDATE agent_quota_relay
SET outcome = 'waiting', wait_reason = $2, updated_at = now()
WHERE source_task_id = $1 AND outcome = 'pending';

-- name: ListPendingQuotaRelays :many
SELECT * FROM agent_quota_relay
WHERE outcome = 'pending'
ORDER BY created_at, id
LIMIT $1;

-- name: UpsertQuotaBreaker :one
INSERT INTO agent_quota_breaker (
    workspace_id, agent_id, scope, model_key, reason, source_task_id,
    detail, recover_at, recover_condition, suppressed_work, suppress_agent_updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, TRUE, sqlc.narg('suppress_agent_updated_at')
)
ON CONFLICT (agent_id, scope, model_key) WHERE recovered_at IS NULL
DO UPDATE SET
    recover_at = GREATEST(agent_quota_breaker.recover_at, EXCLUDED.recover_at),
    recover_condition = EXCLUDED.recover_condition,
    detail = EXCLUDED.detail,
    source_task_id = EXCLUDED.source_task_id,
    suppress_agent_updated_at = COALESCE(EXCLUDED.suppress_agent_updated_at, agent_quota_breaker.suppress_agent_updated_at)
RETURNING *;

-- name: SuppressAgentWorkForQuota :one
UPDATE agent
SET work_enabled = FALSE, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND work_enabled = TRUE AND archived_at IS NULL
RETURNING updated_at;

-- name: ListOpenQuotaBreakerAgentIDs :many
SELECT agent_id FROM agent_quota_breaker
WHERE workspace_id = $1 AND recovered_at IS NULL;

-- name: LockIssueForQuotaRelay :one
SELECT * FROM issue
WHERE id = $1
FOR UPDATE;

-- name: LockTaskForQuotaRelay :one
SELECT * FROM agent_task_queue
WHERE id = $1
FOR UPDATE;

-- name: ReassignIssueToAgentIfCurrent :one
UPDATE issue
SET assignee_type = 'agent',
    assignee_id = sqlc.arg('assignee_id'),
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND assignee_type = 'agent'
  AND assignee_id = sqlc.arg('current_assignee_id')
RETURNING *;

-- name: LockDueQuotaBreakers :many
SELECT * FROM agent_quota_breaker
WHERE recovered_at IS NULL AND recover_at <= now()
ORDER BY recover_at, id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkQuotaBreakerRecovered :execrows
UPDATE agent_quota_breaker
SET recovered_at = now()
WHERE id = $1 AND recovered_at IS NULL;

-- name: CountQuotaBreakersSinceSuccess :one
-- How many breakers this seat opened in the last 24 hours, counting only
-- those that came after its latest successful task. Two or more is the
-- repeated-breaker signal: routing stops preferring the seat until it
-- finishes one task, or until those openings age out of the window.
SELECT count(*)::int
FROM agent_quota_breaker b
WHERE b.agent_id = $1
  AND b.opened_at > now() - interval '24 hours'
  AND b.opened_at > COALESCE((
      SELECT max(t.completed_at)
      FROM agent_task_queue t
      WHERE t.agent_id = b.agent_id AND t.status = 'completed'
  ), '-infinity'::timestamptz);

-- name: ListDemotedQuotaAgentIDs :many
-- Seats routing should not prefer. Same rule as CountQuotaBreakersSinceSuccess,
-- for every seat in the workspace. An open breaker already turns work off;
-- this list is what remains after the seat is accepting work again.
SELECT b.agent_id
FROM agent_quota_breaker b
WHERE b.workspace_id = $1
  AND b.opened_at > now() - interval '24 hours'
  AND b.opened_at > COALESCE((
      SELECT max(t.completed_at)
      FROM agent_task_queue t
      WHERE t.agent_id = b.agent_id AND t.status = 'completed'
  ), '-infinity'::timestamptz)
GROUP BY b.agent_id
HAVING count(*) >= 2;

-- name: ListUnstartedIssuesForAgent :many
-- Issues still assigned to a seat that has not begun executing them.
-- A dispatched, running, or waiting task means the run already started.
-- Queued and deferred tasks have not, and an issue with no task at all
-- has not either. Autopilot runs keep their own scheduler.
SELECT i.*
FROM issue i
WHERE i.workspace_id = $1
  AND i.assignee_type = 'agent'
  AND i.assignee_id = $2
  AND i.status IN ('todo', 'in_progress', 'backlog')
  AND i.triage_state IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      WHERE t.issue_id = i.id
        AND t.autopilot_run_id IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      WHERE t.issue_id = i.id
        AND t.status IN ('dispatched', 'running', 'waiting_local_directory')
  )
  -- An in_progress issue that already ran and has nothing queued is not
  -- waiting for a pickup (a coordinator watching its sub-issues, a PR
  -- waiting on review). Moving it would start a run nobody asked for.
  AND (
      i.status <> 'in_progress'
      OR EXISTS (
          SELECT 1 FROM agent_task_queue t
          WHERE t.issue_id = i.id AND t.status IN ('queued', 'deferred')
      )
      OR NOT EXISTS (
          SELECT 1 FROM agent_task_queue t WHERE t.issue_id = i.id
      )
  )
ORDER BY i.number
LIMIT $3;

-- name: ReleaseAgentQuotaSuppression :execrows
-- Re-enable only when every open breaker for the seat is gone and nobody
-- has edited the agent since the breaker turned work off. A manual disable
-- (or any later agent write) moves updated_at and this matches nothing.
UPDATE agent AS a
SET work_enabled = TRUE, updated_at = now()
WHERE a.id = $1
  AND a.work_enabled = FALSE
  AND a.updated_at = (
      SELECT max(b.suppress_agent_updated_at)
      FROM agent_quota_breaker b
      WHERE b.agent_id = a.id AND b.suppress_agent_updated_at IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_quota_breaker open
      WHERE open.agent_id = a.id
        AND open.recovered_at IS NULL
        AND open.suppressed_work
  );

-- name: HasOpenQuotaBreakerForReason :one
-- DENE-870: the owner reminder for a seat that ran out of money goes out
-- once per episode. An open breaker with the same reason means it already
-- went out.
SELECT EXISTS (
    SELECT 1 FROM agent_quota_breaker
    WHERE agent_id = $1 AND reason = $2 AND recovered_at IS NULL
);

-- name: ListOpenIssuesForBrokenSeat :many
-- DENE-870: everything a seat whose account ran out of money still holds,
-- including tickets it already started. A ticket with a run on the wire is
-- left alone: that run will fail on the same empty account and relay itself.
SELECT i.*
FROM issue i
WHERE i.workspace_id = $1
  AND i.assignee_type = 'agent'
  AND i.assignee_id = $2
  AND i.status IN ('todo', 'in_progress', 'blocked')
  AND i.triage_state IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      WHERE t.issue_id = i.id
        AND t.autopilot_run_id IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      WHERE t.issue_id = i.id
        AND t.status IN ('dispatched', 'running', 'waiting_local_directory')
  )
ORDER BY i.number
LIMIT $3;

-- name: HasIssueRunHistory :one
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue
    WHERE issue_id = $1 AND status IN ('completed', 'failed', 'cancelled')
);

-- name: CloseManualQuotaBreakers :execrows
-- DENE-870: a balance breaker has no timer. A person turning the seat back
-- on after topping up is the recovery.
UPDATE agent_quota_breaker
SET recovered_at = now()
WHERE agent_id = $1 AND reason = 'balance_exhausted' AND recovered_at IS NULL;

-- name: ListOpenQuotaBreakers :many
SELECT agent_id, reason, detail, recover_condition, recover_at, opened_at
FROM agent_quota_breaker
WHERE workspace_id = $1 AND recovered_at IS NULL
ORDER BY opened_at DESC;

-- name: CountIssueFailuresSinceSuccess :one
-- DENE-870 backoff: how many runs on this issue failed in a row, counting
-- only the last day and only after its latest completed run.
SELECT count(*)::int
FROM agent_task_queue t
WHERE t.issue_id = $1
  AND t.status = 'failed'
  AND t.completed_at > now() - interval '24 hours'
  AND t.completed_at > COALESCE((
      SELECT max(c.completed_at)
      FROM agent_task_queue c
      WHERE c.issue_id = t.issue_id AND c.status = 'completed'
  ), '-infinity'::timestamptz);

-- name: ListQuotaAccountSiblings :many
-- DENE-870: the other seats burning the same account as a seat whose
-- account-level quota or balance ran out. Same runtime (one provider CLI on
-- one machine) and either the same base-role family — the base role and
-- every specialisation under it — or an identical custom_env, which is where
-- an agent binds a numbered account directory. Archived seats are left out.
SELECT * FROM agent a
WHERE a.workspace_id = @workspace_id
  AND a.id <> @agent_id
  AND a.archived_at IS NULL
  AND a.runtime_id = @runtime_id
  AND (
      a.id = @root_id
      OR a.parent_agent_id = @root_id
      OR a.custom_env = @custom_env::jsonb
  )
ORDER BY a.created_at, a.id;

-- name: ListInheritingSpecialisations :many
-- DENE-870: a base role's specialisations that run on its runtime profile.
-- A weekly window or capacity miss on the base role is theirs too.
SELECT * FROM agent
WHERE workspace_id = @workspace_id
  AND parent_agent_id = @parent_agent_id
  AND runtime_inherited
  AND archived_at IS NULL
ORDER BY created_at, id;
