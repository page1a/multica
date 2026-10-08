ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_dispatch_mode_check;
ALTER TABLE agent DROP COLUMN IF EXISTS dispatch_mode;
