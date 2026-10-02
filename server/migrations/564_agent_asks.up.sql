-- Generic, workspace-scoped option questions asked by agents or humans.
CREATE TABLE agent_ask (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    asker_type TEXT NOT NULL CHECK (asker_type IN ('member','agent')),
    asker_id UUID NOT NULL,
    title TEXT NOT NULL CHECK (length(trim(title)) > 0),
    questions JSONB NOT NULL,
    answers JSONB,
    mode TEXT NOT NULL DEFAULT 'needs_you' CHECK (mode IN ('needs_you','side_question')),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','answered','cancelled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at TIMESTAMPTZ
);
CREATE INDEX agent_ask_workspace_status_idx ON agent_ask(workspace_id, status, created_at DESC);
CREATE INDEX agent_ask_asker_idx ON agent_ask(asker_type, asker_id, status);
