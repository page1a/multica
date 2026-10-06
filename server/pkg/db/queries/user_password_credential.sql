-- name: GetPasswordCredentialByUsername :one
SELECT * FROM user_password_credential
WHERE lower(username) = lower(sqlc.arg('username'));

-- name: CreatePasswordCredential :one
INSERT INTO user_password_credential (user_id, username, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdatePasswordCredentialHash :one
UPDATE user_password_credential
SET password_hash = sqlc.arg('password_hash'), updated_at = now()
WHERE user_id = sqlc.arg('user_id')
RETURNING *;

-- name: GetPasswordCredentialByUserID :one
SELECT * FROM user_password_credential
WHERE user_id = $1;

-- name: InsertPasswordResetAudit :exec
INSERT INTO password_reset_audit (user_id, method, actor_user_id, workspace_id, client_ip)
VALUES (sqlc.arg('user_id'), sqlc.arg('method'), sqlc.narg('actor_user_id'), sqlc.narg('workspace_id'), sqlc.arg('client_ip'));

-- name: CountPrivilegedMembershipsOutsideOwner :one
-- Workspaces where the target holds owner/admin but the actor is not an owner.
-- An admin reset there would hand the actor that workspace's privileges.
SELECT count(*) FROM member target
WHERE target.user_id = sqlc.arg('target_user_id')
  AND target.role IN ('owner', 'admin')
  AND NOT EXISTS (
    SELECT 1 FROM member actor
    WHERE actor.workspace_id = target.workspace_id
      AND actor.user_id = sqlc.arg('actor_user_id')
      AND actor.role = 'owner'
  );
