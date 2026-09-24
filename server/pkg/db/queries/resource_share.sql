-- name: AddResourceShare :one
-- Idempotent: ON CONFLICT DO NOTHING returns no row, so the caller fetches
-- the existing share instead of treating a duplicate as a 500.
INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, added_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, resource_type, resource_id, member_id) DO NOTHING
RETURNING *;

-- name: GetResourceShare :one
SELECT * FROM resource_share
WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3 AND member_id = $4;

-- name: RemoveResourceShare :execrows
DELETE FROM resource_share
WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3 AND member_id = $4;

-- name: ListResourceShares :many
SELECT
    rs.id,
    rs.workspace_id,
    rs.resource_type,
    rs.resource_id,
    rs.member_id,
    rs.added_by,
    rs.created_at,
    u.name AS user_name,
    u.email AS user_email,
    u.avatar_url AS user_avatar_url
FROM resource_share rs
JOIN "user" u ON u.id = rs.member_id
WHERE rs.workspace_id = $1 AND rs.resource_type = $2 AND rs.resource_id = $3
ORDER BY rs.created_at ASC;

-- name: ListResourceSharesForMember :many
-- Everything shared directly with one member, loaded once per request into
-- the visibility viewer — the Go twin of the resource_share EXISTS clause in
-- issueVisibilitySQL.
SELECT resource_type, resource_id FROM resource_share
WHERE workspace_id = $1 AND member_id = $2;

-- name: CountResourceShares :one
SELECT count(*) FROM resource_share
WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3;

-- name: DeleteResourceSharesByResource :exec
DELETE FROM resource_share
WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3;

-- name: DeleteResourceSharesByMember :exec
DELETE FROM resource_share
WHERE workspace_id = $1 AND member_id = $2;
