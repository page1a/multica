ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_routing_usage_check;
ALTER TABLE agent DROP COLUMN IF EXISTS routing_usage;
