-- name: UpsertProjectMemoryStatus :one
INSERT INTO project_memory_status (
    project_id, workspace_id, location_key, path, exists_on_disk,
    is_directory, modified_at, observed_at, error, mainline_ref
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (project_id, location_key) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    path = EXCLUDED.path,
    exists_on_disk = EXCLUDED.exists_on_disk,
    is_directory = EXCLUDED.is_directory,
    modified_at = EXCLUDED.modified_at,
    observed_at = EXCLUDED.observed_at,
    error = EXCLUDED.error,
    mainline_ref = EXCLUDED.mainline_ref
RETURNING *;

-- name: ListProjectMemoryStatus :many
SELECT * FROM project_memory_status
WHERE project_id = $1 AND workspace_id = $2
ORDER BY location_key ASC;

-- name: ListProjectMemoryTargets :many
SELECT pr.*
FROM project_resource pr
JOIN project p ON p.id = pr.project_id AND p.workspace_id = pr.workspace_id
WHERE pr.workspace_id = $1
  AND pr.resource_type = 'local_directory'
ORDER BY pr.project_id, pr.position ASC, pr.created_at ASC;
