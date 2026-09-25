-- DENE-763: work_thread owns its bounded context policy. Queue rows carry a
-- snapshot for execution, but ordinary inserts must not reset thread limits.
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
    context_generation = GREATEST(work_thread.context_generation, EXCLUDED.context_generation),
    last_session_id = COALESCE(EXCLUDED.last_session_id, work_thread.last_session_id),
    last_turn_id = EXCLUDED.last_turn_id,
    continuity_break_reason = COALESCE(NULLIF(EXCLUDED.continuity_break_reason, ''), work_thread.continuity_break_reason),
    updated_at = now();
  RETURN NEW;
END;
$$;
