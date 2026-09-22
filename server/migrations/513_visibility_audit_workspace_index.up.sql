-- One statement per file: CREATE INDEX CONCURRENTLY cannot share a migration
-- with anything else. Backs the workspace-scoped, newest-first audit read.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_visibility_audit_workspace_created
    ON visibility_audit(workspace_id, created_at DESC);
