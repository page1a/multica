CREATE INDEX CONCURRENTLY idx_resource_share_member
    ON resource_share (workspace_id, member_id);
