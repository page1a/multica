CREATE UNIQUE INDEX CONCURRENTLY idx_resource_share_resource_member
    ON resource_share (workspace_id, resource_type, resource_id, member_id);
