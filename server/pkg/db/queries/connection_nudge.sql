-- One unanswered ask per repository until a connection covers it (DENE-968).

-- name: InsertConnectionNudge :one
INSERT INTO connection_nudge (workspace_id, repo_key, recipient_id)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, repo_key) DO NOTHING
RETURNING workspace_id, repo_key, recipient_id, inbox_item_id, created_at;

-- name: SetConnectionNudgeInbox :exec
UPDATE connection_nudge
SET inbox_item_id = $3
WHERE workspace_id = $1 AND repo_key = $2;

-- name: ListConnectionNudgesByWorkspace :many
SELECT workspace_id, repo_key, recipient_id, inbox_item_id, created_at
FROM connection_nudge
WHERE workspace_id = $1;

-- name: DeleteConnectionNudge :exec
DELETE FROM connection_nudge
WHERE workspace_id = $1 AND repo_key = $2;
