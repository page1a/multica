-- DENE-1672: what a chat-opened task reports back to its chat.

-- name: GetChatSourceMessage :one
-- The user message an issue was opened in answer to: the chat's newest user
-- message at or before the issue's creation.
SELECT * FROM chat_message
WHERE chat_session_id = @chat_session_id
  AND role = 'user'
  AND created_at <= @before
ORDER BY created_at DESC, id DESC
LIMIT 1;
