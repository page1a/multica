DROP INDEX IF EXISTS idx_work_thread_issue_execution;
DROP INDEX IF EXISTS idx_work_thread_chat_execution;
ALTER TABLE work_thread DROP COLUMN IF EXISTS runtime_id;
ALTER TABLE work_thread DROP COLUMN IF EXISTS model;
ALTER TABLE work_thread DROP COLUMN IF EXISTS permission_mode;
