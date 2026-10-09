DELETE FROM workspace_link_audit WHERE action IN ('set_managed', 'managed_write');
ALTER TABLE workspace_link_audit DROP CONSTRAINT IF EXISTS workspace_link_audit_action_check;
ALTER TABLE workspace_link_audit ADD CONSTRAINT workspace_link_audit_action_check
    CHECK (action IN ('create', 'update_projects', 'accept', 'revoke'));
ALTER TABLE workspace_link DROP COLUMN IF EXISTS managed;
