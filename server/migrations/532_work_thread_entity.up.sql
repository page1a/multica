-- DENE-763: make the queue's continuity key a durable entity shared by
-- Issue and Chat.  The queue remains the turn ledger; this row is the
-- thread-level identity and bounded-context policy.
CREATE TABLE IF NOT EXISTS work_thread (
  id UUID PRIMARY KEY,
  agent_id UUID NOT NULL,
  issue_id UUID,
  chat_session_id UUID,
  context_generation INT NOT NULL DEFAULT 0,
  context_message_limit INT NOT NULL DEFAULT 50,
  context_token_budget INT NOT NULL DEFAULT 12000,
  last_session_id TEXT,
  last_turn_id UUID,
  continuity_break_reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Existing queue rows are the source of truth for the initial entity set.
INSERT INTO work_thread (
  id, agent_id, issue_id, chat_session_id, context_generation,
  context_message_limit, context_token_budget, last_session_id,
  last_turn_id, continuity_break_reason, created_at, updated_at
)
SELECT DISTINCT ON (q.work_thread_id)
  q.work_thread_id, q.agent_id, q.issue_id, q.chat_session_id,
  q.context_generation, q.context_message_limit, q.context_token_budget,
  q.session_id, q.id, q.continuity_break_reason, q.created_at, q.created_at
FROM agent_task_queue q
WHERE q.work_thread_id IS NOT NULL
ORDER BY q.work_thread_id, q.created_at DESC, q.id DESC
ON CONFLICT (id) DO UPDATE SET
  context_generation = EXCLUDED.context_generation,
  context_message_limit = EXCLUDED.context_message_limit,
  context_token_budget = EXCLUDED.context_token_budget,
  last_session_id = EXCLUDED.last_session_id,
  last_turn_id = EXCLUDED.last_turn_id,
  continuity_break_reason = EXCLUDED.continuity_break_reason,
  updated_at = EXCLUDED.updated_at;

CREATE INDEX IF NOT EXISTS idx_work_thread_agent_issue
  ON work_thread(agent_id, issue_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_work_thread_agent_chat
  ON work_thread(agent_id, chat_session_id, updated_at DESC);

CREATE OR REPLACE FUNCTION sync_work_thread_from_task()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.work_thread_id IS NULL THEN
    RETURN NEW;
  END IF;
  INSERT INTO work_thread (
    id, agent_id, issue_id, chat_session_id, context_generation,
    context_message_limit, context_token_budget, last_session_id,
    last_turn_id, continuity_break_reason
  ) VALUES (
    NEW.work_thread_id, NEW.agent_id, NEW.issue_id, NEW.chat_session_id,
    NEW.context_generation, NEW.context_message_limit, NEW.context_token_budget,
    NEW.session_id, NEW.id, NEW.continuity_break_reason
  )
  ON CONFLICT (id) DO UPDATE SET
    issue_id = COALESCE(work_thread.issue_id, EXCLUDED.issue_id),
    chat_session_id = COALESCE(work_thread.chat_session_id, EXCLUDED.chat_session_id),
    context_generation = EXCLUDED.context_generation,
    context_message_limit = EXCLUDED.context_message_limit,
    context_token_budget = EXCLUDED.context_token_budget,
    last_session_id = COALESCE(EXCLUDED.last_session_id, work_thread.last_session_id),
    last_turn_id = EXCLUDED.last_turn_id,
    continuity_break_reason = EXCLUDED.continuity_break_reason,
    updated_at = now();
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS sync_work_thread_from_task ON agent_task_queue;
CREATE TRIGGER sync_work_thread_from_task
AFTER INSERT OR UPDATE OF work_thread_id, context_generation, context_message_limit,
  context_token_budget, session_id, continuity_break_reason ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION sync_work_thread_from_task();
