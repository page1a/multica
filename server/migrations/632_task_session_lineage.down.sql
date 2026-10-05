ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS session_break_reason,
    DROP COLUMN IF EXISTS resumed_from_task_id,
    DROP COLUMN IF EXISTS session_mode;
