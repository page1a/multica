DROP INDEX IF EXISTS idx_agent_task_queue_failure_fingerprint;
ALTER TABLE agent_task_queue
  DROP COLUMN IF EXISTS failure_input_version,
  DROP COLUMN IF EXISTS failure_fingerprint;
