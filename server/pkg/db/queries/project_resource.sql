-- name: ListProjectResources :many
SELECT * FROM project_resource
WHERE project_id = $1
ORDER BY position ASC, created_at ASC;

-- name: ListProjectResourcesInWorkspace :many
-- Workspace-scoped read for the daemon claim path. project_resource carries its
-- own workspace_id, so a corrupt project reference cannot pull another tenant's
-- repository URLs or local paths into a claim response.
SELECT * FROM project_resource
WHERE project_id = $1 AND workspace_id = $2
ORDER BY position ASC, created_at ASC;

-- name: ListProjectResourcesForProjects :many
SELECT * FROM project_resource
WHERE project_id = ANY(sqlc.arg('project_ids')::uuid[])
ORDER BY project_id, position ASC, created_at ASC;

-- name: ListProjectResourcesForProjectsInWorkspace :many
-- Workspace-scoped batch read for the multi-project daemon claim (DENE-523):
-- one query for every project attached to the task, under the same tenant rule
-- as ListProjectResourcesInWorkspace. project_resource carries its own
-- workspace_id, so a corrupt project reference cannot pull another tenant's
-- repository URLs or local paths into a claim response.
SELECT * FROM project_resource
WHERE workspace_id = sqlc.arg('workspace_id') AND project_id = ANY(sqlc.arg('project_ids')::uuid[])
ORDER BY project_id, position ASC, created_at ASC;

-- name: GetProjectResource :one
SELECT * FROM project_resource
WHERE id = $1;

-- name: GetProjectResourceInWorkspace :one
SELECT * FROM project_resource
WHERE id = $1 AND workspace_id = $2;

-- name: CreateProjectResource :one
INSERT INTO project_resource (
    project_id, workspace_id, resource_type, resource_ref, label, position, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: UpdateProjectResource :one
UPDATE project_resource
SET resource_ref = $2,
    label        = $3,
    position     = $4
WHERE id = $1
RETURNING *;

-- name: DeleteProjectResource :exec
DELETE FROM project_resource WHERE id = $1;

-- name: CountProjectResources :one
SELECT count(*) FROM project_resource WHERE project_id = $1;

-- name: GetProjectResourceCounts :many
SELECT project_id, count(*)::bigint AS resource_count
FROM project_resource
WHERE project_id = ANY(sqlc.arg('project_ids')::uuid[])
GROUP BY project_id;

-- name: ListProjectRepoURLs :many
-- The repository URLs a project holds. A workspace repo has no row of its own
-- (it is an entry in workspace.repos, see migration 511), so this join table
-- is what "this repo belongs to that project" means.
SELECT DISTINCT (resource_ref->>'url')::text AS url
FROM project_resource
WHERE workspace_id = $1
  AND project_id = $2
  AND resource_type = 'github_repo'
  AND resource_ref ? 'url';

-- name: ListProjectIDsForRepoURL :many
SELECT DISTINCT project_id
FROM project_resource
WHERE workspace_id = $1
  AND resource_type = 'github_repo'
  AND resource_ref->>'url' = sqlc.arg('url')::text;
