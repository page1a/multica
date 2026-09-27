-- A plan node's origin_id is derived from (workspace, plan key, node key), so
-- "at most one issue per plan node" is a database fact: two concurrent
-- `multica plan apply` runs of the same plan cannot both insert a node.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_origin_plan_unique
    ON issue (origin_id)
    WHERE origin_type = 'plan';
