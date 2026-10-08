-- Sub-issues deliver onto their parent's line (DENE-1537).
--
-- A feature split into sub-issues used to ship as one PR per sub-issue, so
-- the three faces of one feature could never be reviewed in one diff and kun
-- carried half-built states between them. A sub-issue created after this
-- change instead works on its own branch forked from the parent's delivery
-- line and, at `issue close --outcome done`, has its commits merged back into
-- that line. Only the parent opens a PR.
--
-- One row per sub-issue that delivers this way. It is written the first time
-- a daemon able to do the merge-back claims the sub-issue, so a sub-issue
-- that already shipped its own branch, or ran on an older daemon, never gets
-- one and keeps the old behaviour.
CREATE TABLE issue_delivery_line (
    issue_id        UUID PRIMARY KEY REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id    UUID NOT NULL,
    -- The issue whose delivery branch receives this sub-issue's commits: its
    -- parent at the time the line was opened.
    owner_issue_id  UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    -- open: not merged back yet. merged: commits are on the owner's branch.
    -- conflict: the last merge-back hit a real conflict and the sub-issue was
    -- closed blocked; conflict_files names the files.
    status          TEXT NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'merged', 'conflict')),
    branch_name     TEXT,
    source_branch   TEXT,
    merged_tip      TEXT,
    -- [{"sha": "...", "subject": "..."}] — what the last merge-back added.
    commits         JSONB NOT NULL DEFAULT '[]'::jsonb,
    conflict_files  TEXT[] NOT NULL DEFAULT '{}',
    merged_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_issue_delivery_line_owner ON issue_delivery_line (owner_issue_id);
