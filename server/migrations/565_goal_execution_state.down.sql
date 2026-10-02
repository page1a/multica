ALTER TABLE issue_goal
    DROP COLUMN IF EXISTS last_continuation_task_id,
    DROP COLUMN IF EXISTS budget_warning_at,
    DROP COLUMN IF EXISTS max_no_progress_rounds,
    DROP COLUMN IF EXISTS no_progress_rounds;
