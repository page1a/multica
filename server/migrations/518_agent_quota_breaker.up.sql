-- Recoverable quota circuit breaker and the one relay that replaces a
-- spent seat (DENE-771).
--
-- A weekly account failure and a specialisation's own model failure are
-- different rows. Both stop only the seat that spent the quota. Neither
-- writes agent.model, a base role's model, or any other seat.
--
-- The relay row is one per failed task. Pending means the replacement was
-- chosen and the enqueue still has to land; the unique source task is what
-- keeps two sweepers from dispatching twice.

CREATE TABLE agent_quota_breaker (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('agent', 'model')),
    model_key TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL,
    source_task_id UUID,
    detail TEXT NOT NULL DEFAULT '',
    opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recover_at TIMESTAMPTZ NOT NULL,
    recover_condition TEXT NOT NULL DEFAULT '',
    recovered_at TIMESTAMPTZ,
    suppressed_work BOOLEAN NOT NULL DEFAULT TRUE,
    suppress_agent_updated_at TIMESTAMPTZ,
    CHECK (
        (scope = 'agent' AND model_key = '')
        OR (scope = 'model' AND model_key <> '')
    )
);

CREATE UNIQUE INDEX agent_quota_breaker_open_uniq
    ON agent_quota_breaker (agent_id, scope, model_key)
    WHERE recovered_at IS NULL;

CREATE INDEX agent_quota_breaker_due
    ON agent_quota_breaker (recover_at, id)
    WHERE recovered_at IS NULL;

CREATE TABLE agent_quota_relay (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    source_task_id UUID NOT NULL,
    issue_id UUID,
    from_agent_id UUID NOT NULL,
    to_agent_id UUID,
    scope TEXT NOT NULL,
    model_key TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL CHECK (outcome IN (
        'pending',
        'relayed',
        'waiting',
        'skipped_no_issue',
        'skipped_autopilot',
        'skipped_terminal',
        'skipped_parked',
        'skipped_triage',
        'skipped_review',
        'skipped_reassigned',
        'skipped_active'
    )),
    tier_from TEXT NOT NULL DEFAULT '',
    tier_to TEXT NOT NULL DEFAULT '',
    wait_reason TEXT NOT NULL DEFAULT '',
    handoff_note TEXT NOT NULL DEFAULT '',
    audit_comment TEXT NOT NULL DEFAULT '',
    trigger_comment_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_quota_relay_source_uniq
    ON agent_quota_relay (source_task_id);

CREATE INDEX agent_quota_relay_pending
    ON agent_quota_relay (created_at)
    WHERE outcome = 'pending';
