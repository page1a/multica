DROP TABLE IF EXISTS agent_access_request;
DROP TABLE IF EXISTS agent_access_pass;
ALTER TABLE agent DROP COLUMN IF EXISTS doorbell_enabled;
