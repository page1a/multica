-- DENE-763: persist the durable continuity boundary shared by task turns.
-- The queue remains the execution ledger; these columns make the thread
-- identity and bounded context visible to every surface.
ALTER TABLE agent_task_queue
  ADD COLUMN IF NOT EXISTS work_thread_id UUID,
  ADD COLUMN IF NOT EXISTS context_generation INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS context_message_limit INT NOT NULL DEFAULT 50,
  ADD COLUMN IF NOT EXISTS context_token_budget INT NOT NULL DEFAULT 12000,
  ADD COLUMN IF NOT EXISTS continuity_break_reason TEXT;

-- Existing issue tasks that already share a resume chain must share one thread.
WITH issue_threads AS (
  SELECT issue_id, agent_id, gen_random_uuid() AS thread_id
  FROM agent_task_queue
  WHERE issue_id IS NOT NULL
  GROUP BY issue_id, agent_id
)
UPDATE agent_task_queue t
SET work_thread_id = i.thread_id
FROM issue_threads i
WHERE t.issue_id = i.issue_id
  AND t.agent_id = i.agent_id
  AND t.work_thread_id IS NULL;

WITH chat_threads AS (
  SELECT chat_session_id, agent_id, gen_random_uuid() AS thread_id
  FROM agent_task_queue
  WHERE chat_session_id IS NOT NULL
  GROUP BY chat_session_id, agent_id
)
UPDATE agent_task_queue t
SET work_thread_id = c.thread_id
FROM chat_threads c
WHERE t.chat_session_id = c.chat_session_id
  AND t.agent_id = c.agent_id
  AND t.work_thread_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_agent_task_queue_work_thread
  ON agent_task_queue(work_thread_id, created_at, id);

-- A thread may have one running turn plus queued inputs.  The claim query is
-- the serialization fence; a unique index over all non-terminal rows would
-- incorrectly reject those queued inputs.
