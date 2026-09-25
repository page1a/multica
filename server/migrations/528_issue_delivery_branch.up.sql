-- Issue-level canonical delivery (DENE-820).
--
-- One issue may leave several branches behind: a rerun by another seat, a
-- fork because the conversation branch was busy, a human rescue. The
-- platform never said which one was the delivery, so a rescue could be
-- missed or an abandoned line merged. This table records every branch a
-- task reported for an issue and lets exactly one of them be canonical.
CREATE TABLE issue_delivery_branch (
    issue_id      UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id  UUID NOT NULL,
    branch_name   TEXT NOT NULL,
    -- canonical: the one line that ships. rescue: work that must be absorbed
    -- into or discarded from canonical before the PR merges. experiment: a
    -- try-out that never blocks delivery. unclassified: a line a task produced
    -- that nobody has named yet — it is reported, never silently accepted.
    role          TEXT NOT NULL DEFAULT 'unclassified'
                  CHECK (role IN ('canonical', 'rescue', 'experiment', 'unclassified')),
    -- For a non-canonical line: how it ended. NULL means still open.
    resolution    TEXT CHECK (resolution IN ('absorbed', 'discarded')),
    resolved_at   TIMESTAMPTZ,
    -- Local site state after the canonical PR merged or the issue closed.
    cleanup_status TEXT NOT NULL DEFAULT 'live'
                  CHECK (cleanup_status IN ('live', 'cleaned', 'kept')),
    cleanup_note  TEXT,
    cleaned_at    TIMESTAMPTZ,
    first_task_id UUID,
    agent_id      UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issue_id, branch_name)
);

-- One delivery truth per issue.
CREATE UNIQUE INDEX idx_issue_delivery_branch_canonical
    ON issue_delivery_branch (issue_id)
    WHERE role = 'canonical';
