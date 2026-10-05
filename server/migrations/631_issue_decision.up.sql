-- The state card's "已拍板" list (DENE-1328): decisions written by
-- `issue close --decision` / `issue handoff --decision`, or added by a person
-- on the issue page. Every other part of the card is derived from records the
-- issue already keeps (close.* metadata, progress rows, goal checks, comments);
-- decisions get their own rows because people edit and delete them one by
-- one, and the issue metadata bag is capped at 8KB.
CREATE TABLE IF NOT EXISTS issue_decision (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    text TEXT NOT NULL CHECK (length(btrim(text)) > 0),
    source TEXT NOT NULL CHECK (source IN ('close', 'handoff', 'manual')),
    author_type TEXT NOT NULL CHECK (author_type IN ('member', 'agent', 'system')),
    author_id UUID,
    -- clock_timestamp, not now(): one close writes several decisions in one
    -- transaction, and they must read back in the order they were given.
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_issue_decision_issue
    ON issue_decision (issue_id, created_at, id);
