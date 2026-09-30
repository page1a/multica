-- One open ask per repository until a connection covers it (DENE-968).
-- No foreign keys: the application deletes the row when the repository is covered.

CREATE TABLE IF NOT EXISTS connection_nudge (
    workspace_id  UUID NOT NULL,
    repo_key      TEXT NOT NULL,
    recipient_id  UUID NOT NULL,
    inbox_item_id UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, repo_key)
);
