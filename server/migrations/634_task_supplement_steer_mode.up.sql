-- How a run that negotiated task-supplement-v1 takes a mid-run message:
-- 'same' reads it inside the running CLI process, 'restart' stops the CLI and
-- resumes the same session with it (DENE-1349). Rows written before this
-- column existed were all native CLIs.
ALTER TABLE task_supplement_capability
    ADD COLUMN steer_mode TEXT NOT NULL DEFAULT 'same';
