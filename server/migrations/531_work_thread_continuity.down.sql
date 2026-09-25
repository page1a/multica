DROP INDEX IF EXISTS idx_agent_task_queue_work_thread;
ALTER TABLE agent_task_queue
  DROP COLUMN IF EXISTS continuity_break_reason,
  DROP COLUMN IF EXISTS context_token_budget,
  DROP COLUMN IF EXISTS context_message_limit,
  DROP COLUMN IF EXISTS context_generation,
  DROP COLUMN IF EXISTS work_thread_id;
