-- Goal + progress titles (DENE-1037): one progress line per issue / chat plus
-- its history. Writes never bump issue.revision or updated_at — progress is
-- not user-edited content, and a background write must not make a person's
-- in-flight title/description save fail with revision_conflict.

-- name: UpdateIssueProgress :one
-- fallback_only is the parking-summary path: it writes only while nobody has
-- said anything better (no agent report, no close summary). Zero rows means
-- an explicit line already stands.
UPDATE issue
SET progress_text = @text,
    progress_source = @source,
    progress_tone = @tone,
    progress_author_type = @author_type,
    progress_author_id = sqlc.narg('author_id')::uuid,
    progress_updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND (NOT @fallback_only::bool OR progress_source NOT IN ('agent', 'close'))
RETURNING *;

-- name: CreateIssueProgress :exec
INSERT INTO issue_progress (workspace_id, issue_id, text, source, tone, author_type, author_id)
VALUES (@workspace_id, @issue_id, @text, @source, @tone, @author_type, sqlc.narg('author_id')::uuid);

-- name: ListIssueProgress :many
SELECT * FROM issue_progress
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT @row_limit;

-- name: UpdateChatSessionProgress :one
-- fallback_only is the model-summary path: an agent's own line written after
-- @since (the turn's user message) wins over a model summary of that turn.
UPDATE chat_session
SET progress_text = @text,
    progress_source = @source,
    progress_tone = @tone,
    progress_author_type = @author_type,
    progress_author_id = sqlc.narg('author_id')::uuid,
    progress_updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
  AND (NOT @fallback_only::bool
       OR progress_source <> 'agent'
       OR progress_updated_at IS NULL
       OR progress_updated_at < sqlc.narg('since')::timestamptz)
RETURNING *;

-- name: CreateChatSessionProgress :exec
INSERT INTO chat_session_progress (workspace_id, chat_session_id, text, source, tone, author_type, author_id)
VALUES (@workspace_id, @chat_session_id, @text, @source, @tone, @author_type, sqlc.narg('author_id')::uuid);

-- name: ListChatSessionProgress :many
SELECT * FROM chat_session_progress
WHERE chat_session_id = @chat_session_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT @row_limit;

-- name: ListChatRecapMessages :many
-- The chat recap (title + model progress) reads the opening user message and
-- the latest turns. Channel commands and empty kickoffs carry no topic.
(SELECT m.role, m.content, m.created_at, m.failure_reason, m.message_kind
 FROM chat_message m
 WHERE m.chat_session_id = @chat_session_id AND m.role = 'user'
   AND m.message_kind NOT IN ('channel_command', 'onboarding_kickoff')
 ORDER BY m.created_at ASC, m.id ASC
 LIMIT 1)
UNION ALL
(SELECT m.role, m.content, m.created_at, m.failure_reason, m.message_kind
 FROM chat_message m
 WHERE m.chat_session_id = @chat_session_id
   AND m.message_kind NOT IN ('channel_command', 'onboarding_kickoff')
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT @recent_limit);

-- name: CountChatAssistantReplies :one
SELECT count(*)::int FROM chat_message
WHERE chat_session_id = @chat_session_id AND role = 'assistant'
  AND failure_reason IS NULL AND message_kind <> 'no_response';
