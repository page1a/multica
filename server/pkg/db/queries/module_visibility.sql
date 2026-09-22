-- name: GetWorkspaceModuleVisibility :one
SELECT workspace_id, module, visibility, project_id, updated_at
FROM workspace_module_visibility
WHERE workspace_id = $1 AND module = $2;

-- name: ListWorkspaceModuleVisibility :many
SELECT workspace_id, module, visibility, project_id, updated_at
FROM workspace_module_visibility
WHERE workspace_id = $1
ORDER BY module;

-- name: UpsertWorkspaceModuleVisibility :one
INSERT INTO workspace_module_visibility (
    workspace_id, module, visibility, project_id, updated_at
) VALUES (
    $1, $2, $3, $4, now()
)
ON CONFLICT (workspace_id, module)
DO UPDATE SET
    visibility = EXCLUDED.visibility,
    project_id = EXCLUDED.project_id,
    updated_at = now()
RETURNING workspace_id, module, visibility, project_id, updated_at;
