-- name: RecordVisibilityChange :one
INSERT INTO visibility_audit (
    workspace_id, actor_type, actor_id, resource_type, resource_id,
    previous_visibility, new_visibility, audience_size, source
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: RecordVisibilityChangesBulk :exec
-- One statement for a whole project sweep. A project can hold thousands of
-- issues and each one needs its own findable audit row; a round trip per row
-- would make the sweep's cost the audit's cost.
INSERT INTO visibility_audit (
    workspace_id, actor_type, actor_id, resource_type, resource_id,
    previous_visibility, new_visibility, audience_size, source
)
SELECT
    sqlc.arg('workspace_id')::uuid,
    sqlc.arg('actor_type')::text,
    sqlc.arg('actor_id')::uuid,
    sqlc.arg('resource_type')::text,
    ids.resource_id,
    prev.previous_visibility,
    sqlc.arg('new_visibility')::text,
    sqlc.arg('audience_size')::integer,
    sqlc.arg('source')::text
FROM unnest(sqlc.arg('resource_ids')::text[]) WITH ORDINALITY AS ids(resource_id, ord)
JOIN unnest(sqlc.arg('previous_visibilities')::text[]) WITH ORDINALITY AS prev(previous_visibility, ord)
  ON prev.ord = ids.ord;

-- name: ListVisibilityAuditForResource :many
SELECT * FROM visibility_audit
WHERE workspace_id = $1 AND resource_type = $2 AND resource_id = $3
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: ListVisibilityAuditForWorkspace :many
SELECT * FROM visibility_audit
WHERE workspace_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;
