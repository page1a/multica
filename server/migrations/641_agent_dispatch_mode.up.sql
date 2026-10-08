-- dispatch_mode is whether automatic dispatch may pick this seat (DENE-1600,
-- ADR-0008). routing_tier only says how strong a seat is; tagging a tier no
-- longer implies joining the auto-dispatch pool.
--
-- auto         — the default; routing, quota relay and seat relay may pick it.
-- mention_only — taken by @mention, assignment and delegation, never picked
--                automatically. Orthogonal to work_enabled, which refuses
--                even a mention.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS dispatch_mode TEXT NOT NULL DEFAULT 'auto';

ALTER TABLE agent
    DROP CONSTRAINT IF EXISTS agent_dispatch_mode_check;
ALTER TABLE agent
    ADD CONSTRAINT agent_dispatch_mode_check
    CHECK (dispatch_mode IN ('auto', 'mention_only'));
