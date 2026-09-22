-- "Show me every scope change this resource ever had" — the lookup the
-- acceptance criterion "any sharing change can be located in the audit log"
-- is checked with.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_visibility_audit_resource
    ON visibility_audit(workspace_id, resource_type, resource_id, created_at DESC);
