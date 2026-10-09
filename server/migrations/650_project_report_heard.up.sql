-- DENE-1667: "听汇报" — where a person has heard a project up to.
--
-- One row per report delivered: who heard (user_id), which project, and the
-- moment the report covers up to (heard_until). The person's cursor for a
-- project is their newest row, so it is kept per person + project and never
-- per chat: hearing a project's report in one chat (or on the project page)
-- moves the same cursor every other chat reads.
--
-- task_id / chat_session_id say which chat run delivered it. A chat turn that
-- delivered reports gets its follow-up buttons from actions (the tickets that
-- were waiting on the person) instead of the model suggestion pass.
--
-- The table is new and empty, so its indexes are built in the same migration.
-- References stay soft (no FKs), like chat_session_project.
CREATE TABLE IF NOT EXISTS project_report_heard (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    project_id UUID NOT NULL,
    heard_since TIMESTAMPTZ NOT NULL,
    heard_until TIMESTAMPTZ NOT NULL,
    item_count INTEGER NOT NULL DEFAULT 0,
    task_id UUID,
    chat_session_id UUID,
    actions JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_project_report_heard_cursor
    ON project_report_heard (user_id, project_id, heard_until DESC);

CREATE INDEX IF NOT EXISTS idx_project_report_heard_task
    ON project_report_heard (task_id)
    WHERE task_id IS NOT NULL;
