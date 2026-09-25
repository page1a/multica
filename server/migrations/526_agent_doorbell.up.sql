-- Agent borrowing: doorbell approvals + time-limited access passes (DENE-808).
--
-- An agent runs on its owner's machine, so letting another member trigger it
-- lends them the owner's keys, logins and files. The fixed public_to
-- allow-list (migration 130) is either open (easy to forget) or closed (the
-- colleague cannot work). This adds the middle state: locked by default, a
-- non-authorised member RINGS instead of being refused, and the owner opens
-- the door once or for a bounded time.
--
--   * agent.doorbell_enabled — per-agent switch. Off keeps today's plain
--     refusal; on turns a would-be refusal into a pending request.
--   * agent_access_request  — one ring: who, which agent, on which issue /
--     comment, and how it was resolved. Expires when nobody answers.
--   * agent_access_pass     — a time-limited grant for one member on one
--     agent. Honoured by the invoke gate exactly like a member target on the
--     allow-list until expires_at or revoked_at.

ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS doorbell_enabled BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS agent_access_pass (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    granted_by UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    request_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agent_access_pass_active
    ON agent_access_pass (agent_id, user_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_agent_access_pass_workspace_user
    ON agent_access_pass (workspace_id, user_id)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_access_request (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    requester_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    comment_id UUID REFERENCES comment(id) ON DELETE SET NULL,
    trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('mention', 'assign')),
    summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'declined', 'expired')),
    resolved_by UUID,
    resolved_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One open ring per (agent, requester, issue): a second @mention while the
-- first is still unanswered joins it instead of paging the owner again.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_access_request_one_pending
    ON agent_access_request (agent_id, requester_id, issue_id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_agent_access_request_workspace_status
    ON agent_access_request (workspace_id, status, created_at DESC);
