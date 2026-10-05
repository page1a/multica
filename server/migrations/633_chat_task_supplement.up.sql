-- Chat runs negotiate the same task-supplement capability as issue runs. A
-- chat task has no issue, so the capability row keeps issue_id empty.
ALTER TABLE task_supplement_capability ALTER COLUMN issue_id DROP NOT NULL;

-- Delivery receipts for chat messages steered into a running reply. The
-- message is sent as an ordinary queued follow-up (followup_task_id) and this
-- row asks the running turn (task_id) to read it first. Delivered: the
-- follow-up is absorbed into the running turn. Not delivered: the follow-up
-- simply runs after the reply, so a lost race never drops the message.
-- No foreign keys, matching task_supplement: teardown stays application-owned.
CREATE TABLE chat_task_supplement (
    task_id UUID NOT NULL,
    chat_message_id UUID NOT NULL,
    followup_task_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    chat_session_id UUID NOT NULL,
    author_id UUID,
    status TEXT NOT NULL CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
    failure_reason TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    PRIMARY KEY (chat_message_id, task_id)
);

CREATE INDEX chat_task_supplement_task_idx ON chat_task_supplement (task_id, status);
CREATE INDEX chat_task_supplement_followup_idx ON chat_task_supplement (followup_task_id);
CREATE INDEX chat_task_supplement_workspace_idx ON chat_task_supplement (workspace_id);
