-- One row per "this ticket needs this person" call (DENE-880). The summon
-- entry writes the inbox row, the subscription and the visible @ comment;
-- this table is what makes the call dedupe-able and answerable: an open row
-- (answered_at IS NULL) blocks a second call to the same person on the same
-- ticket, and the person's next comment on the ticket answers it and wakes
-- the executor. The "等你" list reads the open rows.
CREATE TABLE issue_summon (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    recipient_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    caller_type TEXT NOT NULL CHECK (caller_type IN ('member', 'agent', 'system')),
    caller_id UUID,
    source TEXT NOT NULL,
    reason TEXT NOT NULL,
    comment_id UUID,
    inbox_item_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at TIMESTAMPTZ,
    answer_comment_id UUID,
    reminded_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_issue_summon_open
    ON issue_summon (issue_id, recipient_id)
    WHERE answered_at IS NULL;

CREATE INDEX idx_issue_summon_recipient_open
    ON issue_summon (workspace_id, recipient_id, created_at DESC)
    WHERE answered_at IS NULL;

CREATE INDEX idx_issue_summon_answer_comment
    ON issue_summon (answer_comment_id)
    WHERE answer_comment_id IS NOT NULL;
