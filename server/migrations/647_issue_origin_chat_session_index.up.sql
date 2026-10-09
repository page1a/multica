-- Lists the issues a chat opened (`multica chat tickets`). Only chat-opened
-- issues carry the pointer, so a partial index stays the size of that set.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_origin_chat_session
ON issue (origin_chat_session_id, created_at)
WHERE origin_chat_session_id IS NOT NULL;
