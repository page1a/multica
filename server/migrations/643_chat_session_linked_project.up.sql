-- DENE-1643: a chat can attach projects that arrive through a workspace link
-- (ADR-0004) as read-only references. They live apart from
-- chat_session_project on purpose: that set is the chat's own projects, its
-- head is mirrored onto chat_session.project_id and decides the run's code
-- source. A linked project must never be either.
--
-- link_id + project_id is the reference; every write and every daemon claim
-- re-checks it through internal/workspacelink, so a revoked link or an
-- unticked project stops reaching the agent on the next run. title and
-- source_name are snapshots taken at selection time, only so the UI can still
-- name an entry it now marks as unavailable.
--
-- The table is new and empty, so its indexes are built in the same migration.
-- References stay soft (no FKs), like chat_session_project.
CREATE TABLE chat_session_linked_project (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    link_id UUID NOT NULL,
    project_id UUID NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    source_name TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX chat_session_linked_project_unique
    ON chat_session_linked_project (chat_session_id, link_id, project_id);
