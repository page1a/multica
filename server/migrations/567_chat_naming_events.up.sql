CREATE TABLE chat_naming_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('server_llm', 'runtime', 'rules')),
    status TEXT NOT NULL CHECK (status IN ('success', 'failure')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX chat_naming_event_workspace_created_idx
    ON chat_naming_event (workspace_id, created_at DESC);
