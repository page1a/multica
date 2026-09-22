-- Rolling this feature back restores "every member may enter every module",
-- which is the pre-feature status quo. Module audit rows are dropped first
-- so the tighter resource_type CHECK can be restored.
DELETE FROM visibility_audit WHERE resource_type = 'module';

ALTER TABLE visibility_audit
    DROP CONSTRAINT IF EXISTS visibility_audit_resource_type_check;

ALTER TABLE visibility_audit
    ADD CONSTRAINT visibility_audit_resource_type_check
        CHECK (resource_type IN ('issue', 'project', 'repo'));

DROP TABLE IF EXISTS workspace_module_visibility;
