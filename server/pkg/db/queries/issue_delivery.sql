-- Issue-level canonical delivery (DENE-820). See migration 528.

-- name: ListIssueDeliveryBranches :many
SELECT * FROM issue_delivery_branch
WHERE issue_id = $1
ORDER BY (role = 'canonical') DESC, created_at ASC;

-- name: GetIssueCanonicalDeliveryBranch :one
SELECT * FROM issue_delivery_branch
WHERE issue_id = $1 AND role = 'canonical';

-- name: GetIssueDeliveryBranch :one
SELECT * FROM issue_delivery_branch
WHERE issue_id = $1 AND branch_name = $2;

-- name: InsertIssueDeliveryBranch :one
-- Records a branch the first time it is seen for an issue. A second insert of
-- the same (issue, branch) is a no-op so a replayed task report never
-- re-classifies a line someone already named; the caller reads the existing
-- row when no row comes back.
INSERT INTO issue_delivery_branch (
    issue_id, workspace_id, branch_name, role, first_task_id, agent_id
)
VALUES (
    sqlc.arg('issue_id'), sqlc.arg('workspace_id'), sqlc.arg('branch_name'),
    sqlc.arg('role'), sqlc.narg('first_task_id'), sqlc.narg('agent_id')
)
ON CONFLICT (issue_id, branch_name) DO NOTHING
RETURNING *;

-- name: DemoteIssueCanonicalDeliveryBranch :exec
-- The line that was canonical becomes an open rescue: its work still has to
-- be absorbed into, or discarded from, the new canonical before the PR
-- merges. Nothing is silently abandoned by switching lines.
UPDATE issue_delivery_branch
SET role = 'rescue', resolution = NULL, resolved_at = NULL, updated_at = now()
WHERE issue_id = $1 AND role = 'canonical';

-- name: SetIssueDeliveryBranchRole :one
UPDATE issue_delivery_branch
SET role = sqlc.arg('role'),
    resolution = sqlc.narg('resolution'),
    resolved_at = CASE WHEN sqlc.narg('resolution')::text IS NULL THEN NULL ELSE now() END,
    updated_at = now()
WHERE issue_id = sqlc.arg('issue_id') AND branch_name = sqlc.arg('branch_name')
RETURNING *;

-- name: SetIssueDeliveryBranchCleanup :one
UPDATE issue_delivery_branch
SET cleanup_status = sqlc.arg('cleanup_status'),
    cleanup_note = sqlc.narg('cleanup_note'),
    cleaned_at = CASE WHEN sqlc.arg('cleanup_status')::text = 'cleaned' THEN now() ELSE NULL END,
    updated_at = now()
WHERE issue_id = sqlc.arg('issue_id') AND branch_name = sqlc.arg('branch_name')
RETURNING *;
