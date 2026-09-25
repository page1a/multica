-- DENE-763: a continuous thread is scoped to the execution carrier as well
-- as the agent. Switching runtimes must not resume a provider session created
-- on a different runtime.
ALTER TABLE work_thread
  ADD COLUMN IF NOT EXISTS runtime_id UUID,
  ADD COLUMN IF NOT EXISTS model TEXT,
  ADD COLUMN IF NOT EXISTS permission_mode TEXT;

UPDATE work_thread wt
SET runtime_id = (
  SELECT q.runtime_id
  FROM agent_task_queue q
  WHERE q.work_thread_id = wt.id AND q.runtime_id IS NOT NULL
  ORDER BY q.created_at DESC, q.id DESC
  LIMIT 1
)
WHERE wt.runtime_id IS NULL
  AND EXISTS (SELECT 1 FROM agent_task_queue q WHERE q.work_thread_id = wt.id AND q.runtime_id IS NOT NULL);

UPDATE work_thread wt
SET model = a.model, permission_mode = a.permission_mode
FROM agent a
WHERE a.id = wt.agent_id AND (wt.model IS NULL OR wt.permission_mode IS NULL);

CREATE INDEX IF NOT EXISTS idx_work_thread_issue_execution
  ON work_thread(issue_id, agent_id, runtime_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_work_thread_chat_execution
  ON work_thread(chat_session_id, agent_id, runtime_id, updated_at DESC);

CREATE OR REPLACE FUNCTION sync_work_thread_from_task()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  agent_model TEXT;
  agent_permission TEXT;
BEGIN
  IF NEW.work_thread_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT a.model, a.permission_mode INTO agent_model, agent_permission
  FROM agent a WHERE a.id = NEW.agent_id;
  INSERT INTO work_thread (
    id, agent_id, runtime_id, model, permission_mode, issue_id, chat_session_id, context_generation,
    context_message_limit, context_token_budget, last_session_id,
    last_turn_id, continuity_break_reason
  ) VALUES (
    NEW.work_thread_id, NEW.agent_id, NEW.runtime_id, agent_model, agent_permission, NEW.issue_id,
    NEW.chat_session_id, NEW.context_generation, NEW.context_message_limit,
    NEW.context_token_budget, NEW.session_id, NEW.id, NEW.continuity_break_reason
  )
  ON CONFLICT (id) DO UPDATE SET
    agent_id = EXCLUDED.agent_id,
    runtime_id = COALESCE(EXCLUDED.runtime_id, work_thread.runtime_id),
    model = COALESCE(EXCLUDED.model, work_thread.model),
    permission_mode = COALESCE(EXCLUDED.permission_mode, work_thread.permission_mode),
    issue_id = COALESCE(work_thread.issue_id, EXCLUDED.issue_id),
    chat_session_id = COALESCE(work_thread.chat_session_id, EXCLUDED.chat_session_id),
    context_generation = GREATEST(work_thread.context_generation, EXCLUDED.context_generation),
    last_session_id = COALESCE(EXCLUDED.last_session_id, work_thread.last_session_id),
    last_turn_id = EXCLUDED.last_turn_id,
    continuity_break_reason = COALESCE(NULLIF(EXCLUDED.continuity_break_reason, ''), work_thread.continuity_break_reason),
    updated_at = now();
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS sync_work_thread_from_task ON agent_task_queue;
CREATE TRIGGER sync_work_thread_from_task
AFTER INSERT OR UPDATE OF work_thread_id, context_generation,
  context_message_limit, context_token_budget, session_id,
  continuity_break_reason, runtime_id ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION sync_work_thread_from_task();
