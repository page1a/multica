-- One row per delivery that wrote project memory (DENE-1661): an issue close
-- whose knowledge audit named locations, or a chat that ran `multica chat
-- sediment`. changes holds [{location, summary, files}]; files are the
-- delivered paths the server bound to each location. Exactly one of issue_id
-- / chat_session_id is set. Additive table; nothing existing reads it.
CREATE TABLE knowledge_sediment (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    project_id UUID REFERENCES project(id) ON DELETE SET NULL,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    chat_session_id UUID REFERENCES chat_session(id) ON DELETE CASCADE,
    changes JSONB NOT NULL DEFAULT '[]'::jsonb,
    verified BOOLEAN NOT NULL DEFAULT FALSE,
    mainline TEXT NOT NULL DEFAULT '',
    commits JSONB NOT NULL DEFAULT '[]'::jsonb,
    pr_url TEXT NOT NULL DEFAULT '',
    author_type TEXT NOT NULL,
    author_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((issue_id IS NULL) <> (chat_session_id IS NULL))
);

CREATE INDEX knowledge_sediment_project_created_idx
    ON knowledge_sediment (project_id, created_at DESC);
CREATE INDEX knowledge_sediment_issue_idx ON knowledge_sediment (issue_id);
CREATE INDEX knowledge_sediment_chat_idx ON knowledge_sediment (chat_session_id);
