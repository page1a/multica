-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, id)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()))
RETURNING *;

-- name: GetTaskTokenActorByHash :one
-- The one lookup the auth middleware performs for an `mat_` token, plus the
-- carrier identity behind it.
--
-- agent.kind / agent.system_key ride along on the same round trip because the
-- middleware has to decide the request's capability scope before any handler
-- runs: hidden per-session carriers (currently the `issue_draft:*` alignment
-- conversation) hold a deliberately narrower token than an ordinary task, and a
-- second query on every authenticated agent request to learn that would buy
-- nothing. The join is on the primary key of agent, and agent_id carries an
-- index of its own, so this is still a single index lookup per request.
--
-- task_kind is the run's context.type (DENE-1721): a consult run answers one
-- question and may only read, so its scope is decided here as well. The task
-- join is on agent_task_queue's primary key.
SELECT tt.id,
       tt.token_hash,
       tt.task_id,
       tt.agent_id,
       tt.workspace_id,
       tt.user_id,
       tt.expires_at,
       tt.created_at,
       a.kind AS agent_kind,
       a.system_key AS agent_system_key,
       COALESCE(q.context->>'type', '')::text AS task_kind
FROM task_token tt
JOIN agent a ON a.id = tt.agent_id
LEFT JOIN agent_task_queue q ON q.id = tt.task_id
WHERE tt.token_hash = $1 AND tt.expires_at > now();

-- name: DeleteTaskTokensByTask :exec
DELETE FROM task_token WHERE task_id = $1;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token WHERE expires_at <= now();
