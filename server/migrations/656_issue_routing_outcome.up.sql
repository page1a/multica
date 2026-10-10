-- DENE-1722: routing learns from outcomes. One row per ticket the rule table
-- tiered: the class it was judged in (direction, tier, and the analysis
-- facts) and the two real events that say the tier was too low — the
-- executor escalated, or acceptance held it. A class whose recent tickets
-- were judged low often enough gets its next tickets one rung higher.
--
-- No foreign keys (repository rule): readers skip rows whose issue is gone.
-- A new table; the previous release never reads it.
CREATE TABLE IF NOT EXISTS issue_routing_outcome (
    issue_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    direction TEXT NOT NULL,
    tier TEXT NOT NULL,
    scope TEXT NOT NULL,
    clarity TEXT NOT NULL,
    risk TEXT NOT NULL,
    routed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    escalated_at TIMESTAMPTZ,
    held_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS issue_routing_outcome_workspace_routed_idx
    ON issue_routing_outcome (workspace_id, routed_at);
