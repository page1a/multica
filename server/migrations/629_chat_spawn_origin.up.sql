-- DENE-1271: a chat agent can open a new chat. The child records where it
-- came from; the parent keeps a card that links to the child.
ALTER TABLE chat_session
    ADD COLUMN origin_type TEXT NULL CHECK (origin_type IN ('chat')),
    ADD COLUMN origin_session_id UUID NULL REFERENCES chat_session(id) ON DELETE SET NULL,
    ADD COLUMN origin_task_id UUID NULL,
    ADD COLUMN origin_client_key TEXT NULL;

CREATE INDEX chat_session_origin_session_idx
    ON chat_session (origin_session_id) WHERE origin_session_id IS NOT NULL;

-- A retried spawn from the same run with the same client key finds the
-- session it already created instead of opening a second one.
CREATE UNIQUE INDEX chat_session_origin_client_key_idx
    ON chat_session (origin_task_id, origin_client_key)
    WHERE origin_task_id IS NOT NULL AND origin_client_key IS NOT NULL;

ALTER TABLE chat_message
    ADD COLUMN linked_session_id UUID NULL REFERENCES chat_session(id) ON DELETE SET NULL;

-- One row per thing an agent run created (an issue or a chat), so the
-- per-run budget in workspace.settings.agent_spawn can be counted no matter
-- which entry created it (issue create, plan apply, to-goal, chat open).
CREATE TABLE agent_spawn_record (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    task_id UUID NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('chat', 'issue')),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('chat', 'issue')),
    target_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX agent_spawn_record_task_idx
    ON agent_spawn_record (task_id, target_kind);
