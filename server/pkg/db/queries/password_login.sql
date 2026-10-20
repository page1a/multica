-- name: GetPasswordCredentialByUsername :one
SELECT user_id, username, password_hash, created_at, updated_at
FROM user_password_credential
WHERE lower(username) = lower(sqlc.arg('username'));
