-- DENE-1721: a task run asks a strong seat one question and waits for the
-- answer without handing the ticket over. One row per consult; the answer is
-- produced by an issue-less run of the advisor (agent_task_queue.context.type
-- = 'consult'). Pending and answered rows count against the ticket's limit
-- (workspace.settings.agent_spawn.consult.per_issue); failed ones do not.
--
-- No foreign keys (repository rule): readers join issue and agent and skip
-- rows whose side is gone. A new table; the previous release never reads it.
CREATE TABLE IF NOT EXISTS issue_consult (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    asker_agent_id UUID NOT NULL,
    asker_task_id UUID,
    advisor_agent_id UUID NOT NULL,
    advisor_task_id UUID,
    question TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'answered', 'failed')),
    answer TEXT,
    failure_reason TEXT,
    tokens_used BIGINT NOT NULL DEFAULT 0,
    duration_seconds BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_issue_consult_issue ON issue_consult (issue_id, created_at);
