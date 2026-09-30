CREATE TABLE chat_session_link_read_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    chat_session_id uuid NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    reader_workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    reader_user_id uuid NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    reader_agent_id uuid REFERENCES agent(id) ON DELETE SET NULL,
    reader_task_id uuid REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_session_link_read_audit_session
    ON chat_session_link_read_audit (chat_session_id, created_at DESC);
