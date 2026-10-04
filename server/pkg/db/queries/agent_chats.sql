-- name: ListAgentChatSessions :many
-- The agent overview's Chats section (DENE-1310): every open chat with this
-- agent, running then queued then idle, so the page can tell who holds its concurrency.
-- viewer_can_see is the same rule as ListChatSessionsByCreator (own chat,
-- workspace chat for a non-guest, project chat for a project member, or a
-- share); the handler blanks title and creator on rows where it is false.
SELECT cs.id,
       cs.creator_id,
       cs.title,
       cs.updated_at,
       lm.created_at AS last_message_at,
       COALESCE(act.running, false)::bool AS running,
       COALESCE(act.active, false)::bool AS active,
       (
         cs.creator_id = sqlc.arg(viewer_id)
         OR (cs.visibility = 'workspace' AND EXISTS (
           SELECT 1 FROM member m
            WHERE m.workspace_id = cs.workspace_id
              AND m.user_id = sqlc.arg(viewer_id)
              AND m.role <> 'guest'))
         OR (cs.visibility = 'project' AND (
           EXISTS (
             SELECT 1 FROM resource_share rs
              WHERE rs.workspace_id = cs.workspace_id
                AND rs.resource_type = 'chat_session'
                AND rs.resource_id = cs.id::text
                AND rs.member_id = sqlc.arg(viewer_id))
           OR EXISTS (
             SELECT 1 FROM chat_session_project csp
              WHERE csp.chat_session_id = cs.id
                AND csp.project_id = ANY(sqlc.arg(project_ids)::uuid[]))
           OR (cs.project_id IS NOT NULL AND cs.project_id = ANY(sqlc.arg(project_ids)::uuid[]))
         ))
       )::bool AS viewer_can_see
FROM chat_session cs
LEFT JOIN LATERAL (
  SELECT m.created_at
    FROM chat_message m
   WHERE m.chat_session_id = cs.id
     AND m.message_kind != 'channel_command'
   ORDER BY m.created_at DESC
   LIMIT 1
) lm ON true
LEFT JOIN LATERAL (
  SELECT bool_or(t.status = 'running') AS running, true AS active
    FROM agent_task_queue t
   WHERE t.chat_session_id = cs.id
     AND t.agent_id = cs.agent_id
     AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
  HAVING count(*) > 0
) act ON true
WHERE cs.workspace_id = sqlc.arg(workspace_id)
  AND cs.agent_id = sqlc.arg(agent_id)
  AND cs.status = 'active'
  AND (cs.explicitly_created_at IS NOT NULL OR lm.created_at IS NOT NULL)
ORDER BY COALESCE(act.running, false) DESC,
         COALESCE(act.active, false) DESC,
         COALESCE(lm.created_at, cs.updated_at) DESC,
         cs.id DESC
LIMIT sqlc.arg(page_limit);
