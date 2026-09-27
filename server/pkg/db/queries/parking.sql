-- Queries for the per-issue parking record (DENE-881).
--
-- The record is written only at a run's end (completion or failure path); the
-- server runs no scan over it (DENE-520). Reads list the latest record per
-- issue for one workspace.

-- name: CreateIssueRejection :exec
-- A platform refusal that used to exist only as a 4xx body. Kept so the
-- parking record can say "送审被拒：PR 未关联" after the run is gone.
INSERT INTO issue_rejection (id, workspace_id, issue_id, actor_type, actor_id, action, kind, reason)
VALUES (
    sqlc.arg('id')::uuid,
    sqlc.arg('workspace_id')::uuid,
    sqlc.arg('issue_id')::uuid,
    sqlc.arg('actor_type')::text,
    sqlc.narg('actor_id')::uuid,
    sqlc.arg('action')::text,
    sqlc.arg('kind')::text,
    sqlc.arg('reason')::text
);

-- name: ListIssueRejectionsSince :many
SELECT id, action, kind, reason, actor_type, actor_id, created_at
FROM issue_rejection
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND created_at >= sqlc.arg('since')::timestamptz
ORDER BY created_at ASC
LIMIT 20;

-- name: ListRecentTasksForParking :many
-- The newest runs on the issue, newest first, for the parking timeline.
SELECT id, agent_id, status, created_at, started_at, completed_at, failure_reason, error
FROM agent_task_queue
WHERE issue_id = sqlc.arg('issue_id')::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg('row_limit')::int;

-- name: ListPullRequestsForParking :many
SELECT pr.pr_number, pr.state, pr.html_url, pr.merged_at, pr.pr_created_at, ipr.linked_at
FROM issue_pull_request ipr
JOIN github_pull_request pr ON pr.id = ipr.pull_request_id
WHERE ipr.issue_id = sqlc.arg('issue_id')::uuid
ORDER BY ipr.linked_at ASC;

-- name: GetLatestAgentCommentForTask :one
-- What the agent said during one run: the newest agent-authored comment the
-- run posted, falling back to one written after the run started.
SELECT id, content, created_at
FROM comment
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND author_type = 'agent'
  AND (source_task_id = sqlc.arg('task_id')::uuid OR created_at >= sqlc.arg('since')::timestamptz)
ORDER BY created_at DESC
LIMIT 1;

-- name: GetIssueParkingRecord :one
SELECT * FROM issue_parking_record WHERE issue_id = sqlc.arg('issue_id')::uuid;

-- name: UpsertIssueParkingRecord :one
INSERT INTO issue_parking_record (
    issue_id, workspace_id, state, category, stuck_kind, unexplained, summary,
    summary_source, next_owner_type, next_owner_id, issue_status, task_id,
    timeline, evaluated_at
) VALUES (
    sqlc.arg('issue_id')::uuid,
    sqlc.arg('workspace_id')::uuid,
    sqlc.arg('state')::text,
    sqlc.arg('category')::text,
    sqlc.arg('stuck_kind')::text,
    sqlc.arg('unexplained')::boolean,
    sqlc.arg('summary')::text,
    sqlc.arg('summary_source')::text,
    sqlc.arg('next_owner_type')::text,
    sqlc.arg('next_owner_id')::text,
    sqlc.arg('issue_status')::text,
    sqlc.narg('task_id')::uuid,
    sqlc.arg('timeline')::jsonb,
    now()
)
ON CONFLICT (issue_id) DO UPDATE SET
    state = EXCLUDED.state,
    category = EXCLUDED.category,
    stuck_kind = EXCLUDED.stuck_kind,
    unexplained = EXCLUDED.unexplained,
    summary = EXCLUDED.summary,
    summary_source = EXCLUDED.summary_source,
    next_owner_type = EXCLUDED.next_owner_type,
    next_owner_id = EXCLUDED.next_owner_id,
    issue_status = EXCLUDED.issue_status,
    task_id = EXCLUDED.task_id,
    timeline = EXCLUDED.timeline,
    evaluated_at = now()
RETURNING *;

-- name: ListWorkspaceParkingRecords :many
-- The latest record of every issue in the workspace that has one, with the
-- issue fields a list needs to nest children under their parent. The live
-- issue status is returned beside the recorded one: a record is written at a
-- run's end, so a later human status change shows up as a difference.
SELECT
    pr.issue_id, pr.state, pr.category, pr.stuck_kind, pr.unexplained,
    pr.summary, pr.summary_source, pr.next_owner_type, pr.next_owner_id,
    pr.issue_status AS recorded_status, pr.task_id, pr.timeline, pr.evaluated_at,
    i.number, i.title, i.status AS current_status, i.parent_issue_id
FROM issue_parking_record pr
JOIN issue i ON i.id = pr.issue_id
WHERE pr.workspace_id = sqlc.arg('workspace_id')::uuid
  AND (NOT sqlc.arg('unexplained_only')::boolean OR pr.unexplained)
ORDER BY pr.evaluated_at DESC
LIMIT sqlc.arg('row_limit')::int;
