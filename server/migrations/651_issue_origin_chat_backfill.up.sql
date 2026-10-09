-- DENE-1672: issues opened from a chat before 646 recorded their chat only
-- through the origin. Fill origin_chat_session_id from what the origin already
-- implies, so those chats list their tickets and hear their receipts:
--   agent_create   a chat run's issue (origin_id = the run's task)
--   *_chat         an IM `/issue` command (origin_id = the chat)
--   issue_draft    an alignment's issue (origin_id = the chat)
-- Data-only; a chat that no longer exists is skipped.
UPDATE issue i
SET origin_chat_session_id = t.chat_session_id
FROM agent_task_queue t
WHERE i.origin_type = 'agent_create'
  AND i.origin_id = t.id
  AND t.chat_session_id IS NOT NULL
  AND i.origin_chat_session_id IS NULL
  AND EXISTS (SELECT 1 FROM chat_session cs WHERE cs.id = t.chat_session_id AND cs.workspace_id = i.workspace_id);

UPDATE issue i
SET origin_chat_session_id = cs.id
FROM chat_session cs
WHERE i.origin_type IN ('lark_chat', 'slack_chat', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'issue_draft')
  AND i.origin_id = cs.id
  AND cs.workspace_id = i.workspace_id
  AND i.origin_chat_session_id IS NULL;
