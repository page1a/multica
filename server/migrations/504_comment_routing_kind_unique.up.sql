-- One routing comment of each kind per issue, enforced by the database.
--
-- The routing module checks for an existing comment before posting, but issue
-- creation and the status change that immediately follows can call it twice
-- almost simultaneously; both reads would come back empty and both would post.
-- The insert is written as ON CONFLICT DO NOTHING against this index, so the
-- loser of that race writes nothing instead of a duplicate.
--
-- Partial: ordinary comments carry NULL here and must not be constrained.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS comment_routing_kind_uniq
    ON comment (issue_id, routing_kind)
    WHERE routing_kind IS NOT NULL;
