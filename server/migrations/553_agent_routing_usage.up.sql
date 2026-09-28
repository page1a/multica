-- routing_usage is how much account headroom a person says this seat has:
-- 紧张 / 常规 / 充足, stored as tight / normal / ample. Routing prefers an
-- ample seat inside a tier, and may move one tier up when every seat on the
-- judged tier is tight (DENE-922).
--
-- Human-set, like routing_tier: the server cannot see the account behind a
-- seat, and "how much of it can we spend" is a decision, not a measurement.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS routing_usage TEXT NOT NULL DEFAULT 'normal';

ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_routing_usage_check;
ALTER TABLE agent
    ADD CONSTRAINT agent_routing_usage_check
    CHECK (routing_usage IN ('tight', 'normal', 'ample'));
