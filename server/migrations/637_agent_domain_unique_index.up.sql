-- DENE-1451: one base role has at most one live specialisation per domain, so
-- "孙悟空 on an 出海 issue" always names exactly one seat.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_parent_domain
    ON agent (parent_agent_id, domain_id)
    WHERE parent_agent_id IS NOT NULL AND domain_id IS NOT NULL AND archived_at IS NULL;
