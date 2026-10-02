-- DENE-1052: durable execution state for server-owned goal continuation.
ALTER TABLE issue_goal
    ADD COLUMN no_progress_rounds INTEGER NOT NULL DEFAULT 0 CHECK (no_progress_rounds >= 0),
    ADD COLUMN max_no_progress_rounds INTEGER NOT NULL DEFAULT 3 CHECK (max_no_progress_rounds > 0),
    ADD COLUMN budget_warning_at TIMESTAMPTZ,
    ADD COLUMN last_continuation_task_id UUID;
