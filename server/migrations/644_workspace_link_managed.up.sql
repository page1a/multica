-- DENE-1663: a link's source owner can let the viewer's agents manage the
-- source's issues and autopilots on behalf of the run's originator. Expand
-- only: the old server ignores the column and never writes the new actions.
ALTER TABLE workspace_link ADD COLUMN managed BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE workspace_link_audit DROP CONSTRAINT IF EXISTS workspace_link_audit_action_check;
ALTER TABLE workspace_link_audit ADD CONSTRAINT workspace_link_audit_action_check
    CHECK (action IN ('create', 'update_projects', 'accept', 'revoke', 'set_managed', 'managed_write'));
