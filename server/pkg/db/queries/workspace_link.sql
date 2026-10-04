-- Cross-workspace read-only links (DENE-1225). Only server/internal/workspacelink
-- calls these; the rules about who may run them live there, not here.

-- name: GetWorkspaceLink :one
SELECT * FROM workspace_link WHERE id = $1;

-- name: CreateWorkspaceLink :one
INSERT INTO workspace_link (source_workspace_id, target_workspace_id, created_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: AcceptWorkspaceLink :one
UPDATE workspace_link
SET status = 'active', accepted_by = $2, accepted_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: DeleteWorkspaceLink :exec
DELETE FROM workspace_link WHERE id = $1;

-- name: ListWorkspaceLinksForWorkspace :many
-- Every link the workspace is either side of, with both sides' display names.
SELECT l.*,
       sw.name AS source_name, sw.slug AS source_slug, sw.avatar_url AS source_avatar_url,
       tw.name AS target_name, tw.slug AS target_slug, tw.avatar_url AS target_avatar_url
FROM workspace_link l
JOIN workspace sw ON sw.id = l.source_workspace_id
JOIN workspace tw ON tw.id = l.target_workspace_id
WHERE l.source_workspace_id = sqlc.arg('workspace_id')::uuid
   OR l.target_workspace_id = sqlc.arg('workspace_id')::uuid
ORDER BY l.created_at DESC;

-- name: ListWorkspaceLinkProjects :many
-- The link's projects that still belong to the source workspace. Titles are
-- returned so the source side can show what it shares.
SELECT p.id, p.title, p.icon, p.visibility
FROM workspace_link_project lp
JOIN project p ON p.id = lp.project_id
WHERE lp.link_id = sqlc.arg('link_id')::uuid
  AND p.workspace_id = sqlc.arg('source_workspace_id')::uuid
ORDER BY p.title, p.id;

-- name: ClearWorkspaceLinkProjects :exec
DELETE FROM workspace_link_project WHERE link_id = $1;

-- name: AddWorkspaceLinkProjects :exec
INSERT INTO workspace_link_project (link_id, project_id)
SELECT sqlc.arg('link_id')::uuid, unnest(sqlc.arg('project_ids')::uuid[])
ON CONFLICT DO NOTHING;

-- name: ListShareableProjects :many
-- The subset of project_ids that belong to the workspace and are not private.
SELECT id FROM project
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND id = ANY(sqlc.arg('project_ids')::uuid[])
  AND visibility <> 'private';

-- name: InsertWorkspaceLinkAudit :exec
INSERT INTO workspace_link_audit (link_id, source_workspace_id, target_workspace_id, workspace_id, actor_id, action, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListWorkspaceLinkAudit :many
SELECT a.*, u.name AS actor_name
FROM workspace_link_audit a
LEFT JOIN "user" u ON u.id = a.actor_id
WHERE a.source_workspace_id = sqlc.arg('workspace_id')::uuid
   OR a.target_workspace_id = sqlc.arg('workspace_id')::uuid
ORDER BY a.created_at DESC
LIMIT 200;

-- name: ListLinkedViewProjects :many
-- What a viewer sees of the link's projects: re-checked on every read, so a
-- project made private after it was ticked disappears at once. Counts cover
-- only the issues the viewer could see (non-private, out of triage).
SELECT p.id, p.title, p.icon, p.status,
       count(i.id)::bigint AS total_count,
       count(i.id) FILTER (WHERE i.status = ANY(sqlc.arg('terminal_status_keys')::text[]))::bigint AS done_count
FROM workspace_link_project lp
JOIN project p ON p.id = lp.project_id
LEFT JOIN issue i ON i.project_id = p.id
    AND i.workspace_id = p.workspace_id
    AND i.visibility <> 'private'
    AND i.triage_state IS NULL
WHERE lp.link_id = sqlc.arg('link_id')::uuid
  AND p.workspace_id = sqlc.arg('source_workspace_id')::uuid
  AND p.visibility <> 'private'
GROUP BY p.id
ORDER BY p.title, p.id;

-- name: ListLinkedViewIssues :many
-- The one issue read behind the link view. Every filter that keeps source
-- data in is here: the source workspace, a project still ticked on this link
-- and still not private, and an issue that is not private and not in triage.
-- Paged by (updated_at, number) descending; number is unique per workspace.
SELECT w.issue_prefix, i.number, i.title, i.status, i.priority, i.due_date, i.updated_at,
       p.id AS project_id,
       COALESCE(u.name, a.name, s.name, '')::text AS assignee_name,
       COALESCE(u.avatar_url, a.avatar_url, s.avatar_url) AS assignee_avatar_url
FROM issue i
JOIN workspace w ON w.id = i.workspace_id
JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
JOIN workspace_link_project lp ON lp.project_id = p.id AND lp.link_id = sqlc.arg('link_id')::uuid
LEFT JOIN "user" u ON i.assignee_type = 'member' AND u.id = i.assignee_id
LEFT JOIN agent a ON i.assignee_type = 'agent' AND a.id = i.assignee_id
LEFT JOIN squad s ON i.assignee_type = 'squad' AND s.id = i.assignee_id
WHERE i.workspace_id = sqlc.arg('source_workspace_id')::uuid
  AND p.visibility <> 'private'
  AND i.visibility <> 'private'
  AND i.triage_state IS NULL
  AND (sqlc.narg('project_id')::uuid IS NULL OR p.id = sqlc.narg('project_id')::uuid)
  AND (
    sqlc.narg('cursor_updated_at')::timestamptz IS NULL
    OR (i.updated_at, i.number) < (sqlc.narg('cursor_updated_at')::timestamptz, sqlc.arg('cursor_number')::int)
  )
ORDER BY i.updated_at DESC, i.number DESC
LIMIT sqlc.arg('page_limit')::int;
