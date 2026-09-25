ALTER TABLE agent_task_queue
  ADD COLUMN failure_input_version TEXT,
  ADD COLUMN failure_fingerprint TEXT;

CREATE INDEX idx_agent_task_queue_failure_fingerprint
  ON agent_task_queue (failure_input_version, failure_fingerprint, completed_at)
  WHERE status = 'failed';
