-- name: GetIssueGoal :one
SELECT * FROM issue_goal WHERE issue_id = $1 AND workspace_id = $2;

-- name: CreateIssueGoal :one
INSERT INTO issue_goal (issue_id, workspace_id, status, round, token_limit, run_limit, duration_seconds, created_by_type, created_by_id)
VALUES ($1, $2, 'draft', 0, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListIssueGoalChecks :many
SELECT * FROM issue_goal_check WHERE goal_id = $1 ORDER BY position ASC;

-- name: CreateIssueGoalCheck :one
INSERT INTO issue_goal_check (goal_id, position, description, method)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ConfirmIssueGoal :one
UPDATE issue_goal
SET status = 'active', locked_at = COALESCE(locked_at, now()), updated_at = now(), round = GREATEST(round, 1)
WHERE issue_id = $1 AND workspace_id = $2 AND status = 'draft'
RETURNING *;

-- name: AppendIssueGoalBudget :one
UPDATE issue_goal
SET status = CASE WHEN status = 'stopped' THEN 'active' ELSE status END,
    stopped_at = CASE WHEN status = 'stopped' THEN NULL ELSE stopped_at END,
    token_limit = token_limit + $3,
    run_limit = run_limit + $4,
    duration_seconds = duration_seconds + $5,
    updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2 AND status IN ('draft', 'active', 'stopped')
RETURNING *;

-- name: FinishIssueGoal :one
UPDATE issue_goal
SET status = $3,
    stopped_at = CASE WHEN $3 = 'stopped' THEN now() ELSE stopped_at END,
    achieved_at = CASE WHEN $3 = 'achieved' THEN now() ELSE achieved_at END,
    updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2 AND status IN ('draft', 'active')
RETURNING *;

-- name: UpdateIssueGoalUsage :one
UPDATE issue_goal
SET tokens_used = tokens_used + $3,
    runs_used = runs_used + $4,
    duration_seconds_used = duration_seconds_used + $5,
    round = GREATEST(round, $6),
    updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2
RETURNING *;

-- name: UpdateIssueGoalCheck :one
UPDATE issue_goal_check
SET status = $3,
    evidence = COALESCE($4::jsonb, evidence),
    updated_at = now()
WHERE id = $1 AND goal_id = $2
RETURNING *;

-- name: MarkIssueGoalBudgetWarning :one
UPDATE issue_goal
SET budget_warning_at = COALESCE(budget_warning_at, now()), updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2 AND status = 'active'
RETURNING *;

-- name: StopIssueGoalForBudget :one
UPDATE issue_goal
SET status = 'stopped', stopped_at = COALESCE(stopped_at, now()), updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2 AND status = 'active'
RETURNING *;

-- name: UpdateIssueGoalProgress :one
UPDATE issue_goal
SET no_progress_rounds = $3,
    round = GREATEST(round, $4),
    last_continuation_task_id = COALESCE($5::uuid, last_continuation_task_id),
    updated_at = now()
WHERE issue_id = $1 AND workspace_id = $2 AND status = 'active'
RETURNING *;

-- name: CreateGoalBudgetAsk :one
INSERT INTO agent_ask (workspace_id, issue_id, asker_type, asker_id, title, questions, mode)
VALUES ($1, $2, 'agent', $3, $4, $5, 'needs_you')
RETURNING id;
