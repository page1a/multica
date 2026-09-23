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
