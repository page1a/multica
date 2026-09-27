-- DENE-881: every ticket's latest "parking record" — why it stopped, what the
-- agent last said, who holds the next move — plus the platform refusals that
-- used to live only in an HTTP 4xx response.
--
-- Shipped as 540_issue_parking_record, which collided with 540_issue_summon.
-- Renumbered to 551 (upstream already owns 502–550); every statement is
-- IF NOT EXISTS so installations that already applied it under the old
-- version re-run it as a no-op.

CREATE TABLE IF NOT EXISTS issue_rejection (
    id           UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id     UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    actor_type   TEXT NOT NULL DEFAULT '',
    actor_id     UUID,
    action       TEXT NOT NULL,
    kind         TEXT NOT NULL,
    reason       TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_issue_rejection_issue ON issue_rejection (issue_id, created_at DESC);

CREATE TABLE IF NOT EXISTS issue_parking_record (
    issue_id         UUID PRIMARY KEY REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id     UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    state            TEXT NOT NULL CHECK (state IN ('running', 'parked')),
    category         TEXT NOT NULL,
    stuck_kind       TEXT NOT NULL DEFAULT '',
    unexplained      BOOLEAN NOT NULL DEFAULT FALSE,
    summary          TEXT NOT NULL DEFAULT '',
    summary_source   TEXT NOT NULL DEFAULT '',
    next_owner_type  TEXT NOT NULL DEFAULT '',
    next_owner_id    TEXT NOT NULL DEFAULT '',
    issue_status     TEXT NOT NULL,
    task_id          UUID,
    timeline         JSONB NOT NULL DEFAULT '[]'::jsonb,
    evaluated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_issue_parking_record_workspace ON issue_parking_record (workspace_id, evaluated_at DESC);
