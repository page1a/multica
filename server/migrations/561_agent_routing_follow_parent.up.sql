-- DENE-1016: a following specialisation takes its base role's routing tier
-- and usage. The copy is kept by SyncInheritedAgentRuntimeProfiles on every
-- base-role routing edit and when follow is turned on; this brings the rows
-- that already follow up to date once, instead of waiting for that edit.
-- Archived rows are included: "all followers" means the stored pair matches
-- the parent even for a seat that is not running, so a later restore does
-- not briefly route on the old tag.
UPDATE agent AS child
SET routing_tier = parent.routing_tier,
    routing_usage = parent.routing_usage,
    updated_at = now()
FROM agent AS parent
WHERE child.parent_agent_id = parent.id
  AND child.runtime_inherited
  AND (child.routing_tier IS DISTINCT FROM parent.routing_tier
    OR child.routing_usage IS DISTINCT FROM parent.routing_usage);
