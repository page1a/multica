-- One row per deployment. The boolean primary key rejects a second insert.

-- name: GetGitHubAppCredential :one
SELECT * FROM github_app_credential WHERE id = TRUE;

-- name: InsertGitHubAppCredential :one
INSERT INTO github_app_credential (
    app_id, slug, name, html_url, manage_url, client_id,
    private_key, webhook_secret, client_secret, created_by, workspace_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;

-- name: InsertGitHubAppLaunchToken :exec
INSERT INTO github_app_launch_token (token_hash, state, expires_at)
VALUES ($1, $2, $3);

-- name: DeleteStaleGitHubAppLaunchTokens :exec
DELETE FROM github_app_launch_token WHERE expires_at < $1;

-- Spend the token: only an unused, unexpired row yields its state.
-- name: ConsumeGitHubAppLaunchToken :one
UPDATE github_app_launch_token
SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
RETURNING state;

-- name: GetGitHubAppLaunchToken :one
SELECT * FROM github_app_launch_token WHERE token_hash = $1;
