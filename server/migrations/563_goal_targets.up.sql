-- DENE-1051: an issue may carry one human-confirmed completion line (goal).
CREATE TABLE issue_goal (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id UUID NOT NULL UNIQUE REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'stopped', 'achieved')),
    round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
    token_limit BIGINT NOT NULL DEFAULT 0 CHECK (token_limit >= 0),
    run_limit INTEGER NOT NULL DEFAULT 0 CHECK (run_limit >= 0),
    duration_seconds BIGINT NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    tokens_used BIGINT NOT NULL DEFAULT 0 CHECK (tokens_used >= 0),
    runs_used INTEGER NOT NULL DEFAULT 0 CHECK (runs_used >= 0),
    duration_seconds_used BIGINT NOT NULL DEFAULT 0 CHECK (duration_seconds_used >= 0),
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    created_by_type TEXT NOT NULL DEFAULT 'member' CHECK (created_by_type IN ('member', 'agent', 'system')),
    created_by_id UUID,
    locked_at TIMESTAMPTZ,
    stopped_at TIMESTAMPTZ,
    achieved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX issue_goal_workspace_idx ON issue_goal(workspace_id);

CREATE TABLE issue_goal_check (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    goal_id UUID NOT NULL REFERENCES issue_goal(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    description TEXT NOT NULL CHECK (length(btrim(description)) > 0),
    method TEXT NOT NULL DEFAULT 'acceptance' CHECK (method IN ('command', 'test', 'screenshot', 'acceptance')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'passed', 'failed')),
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (goal_id, position)
);
CREATE INDEX issue_goal_check_goal_idx ON issue_goal_check(goal_id, position);
