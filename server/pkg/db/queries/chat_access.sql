-- Chat sharing (DENE-840). Visibility lives on chat_session.visibility.
-- Extra people live in resource_share (resource_type = 'chat_session')
-- with access 'view' or 'speak'. Project members are not rows here.

-- name: SetChatSessionVisibility :one
UPDATE chat_session
SET visibility = sqlc.arg(visibility), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetChatMessageSender :exec
UPDATE chat_message
SET sender_user_id = sqlc.arg(sender_user_id)
WHERE id = sqlc.arg(id) AND role = 'user';

-- name: UpsertChatSessionRead :exec
INSERT INTO chat_session_read (chat_session_id, user_id, last_read_at)
VALUES (sqlc.arg(chat_session_id), sqlc.arg(user_id), now())
ON CONFLICT (chat_session_id, user_id)
DO UPDATE SET last_read_at = now();

-- name: EnsureVisibleChatReadCursors :exec
-- First time someone else can see a chat, their cursor starts at now so the
-- history they just inherited is not a pile of unread. The creator's first
-- cursor copies chat_session.last_read_at, which older writes still move.
INSERT INTO chat_session_read (chat_session_id, user_id, last_read_at)
SELECT cs.id, sqlc.arg(viewer_id),
       CASE WHEN cs.creator_id = sqlc.arg(viewer_id) THEN cs.last_read_at ELSE now() END
FROM chat_session cs
WHERE cs.workspace_id = sqlc.arg(workspace_id)
  AND (
    cs.creator_id = sqlc.arg(viewer_id)
    OR (
      (cs.visibility = 'workspace' AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = cs.workspace_id AND m.user_id = sqlc.arg(viewer_id) AND m.role <> 'guest'))
      OR (cs.visibility = 'project'
      AND (
        EXISTS (
          SELECT 1 FROM resource_share rs
          WHERE rs.workspace_id = cs.workspace_id
            AND rs.resource_type = 'chat_session'
            AND rs.resource_id = cs.id::text
            AND rs.member_id = sqlc.arg(viewer_id)
        )
        OR EXISTS (
          SELECT 1 FROM chat_session_project csp
          WHERE csp.chat_session_id = cs.id
            AND csp.project_id = ANY(sqlc.arg(project_ids)::uuid[])
        )
        OR (
          cs.project_id IS NOT NULL
          AND cs.project_id = ANY(sqlc.arg(project_ids)::uuid[])
        )
      ))
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM chat_session_read r
    WHERE r.chat_session_id = cs.id AND r.user_id = sqlc.arg(viewer_id)
  )
ON CONFLICT DO NOTHING;

-- name: GetChatShareAccess :one
SELECT access FROM resource_share
WHERE workspace_id = sqlc.arg(workspace_id)
  AND resource_type = 'chat_session'
  AND resource_id = sqlc.arg(resource_id)
  AND member_id = sqlc.arg(member_id);

-- name: ListChatShares :many
SELECT rs.member_id, rs.access, u.name AS user_name, u.email AS user_email
FROM resource_share rs
JOIN "user" u ON u.id = rs.member_id
WHERE rs.workspace_id = sqlc.arg(workspace_id)
  AND rs.resource_type = 'chat_session'
  AND rs.resource_id = sqlc.arg(resource_id)
ORDER BY rs.created_at ASC;

-- name: UpsertChatShare :exec
INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, added_by, access)
VALUES (
  sqlc.arg(workspace_id),
  'chat_session',
  sqlc.arg(resource_id),
  sqlc.arg(member_id),
  sqlc.arg(added_by),
  sqlc.arg(access)
)
ON CONFLICT (workspace_id, resource_type, resource_id, member_id)
DO UPDATE SET access = EXCLUDED.access;

-- name: ChatSessionHasProject :one
SELECT (
  cs.project_id IS NOT NULL
  OR EXISTS (
    SELECT 1 FROM chat_session_project csp
    WHERE csp.chat_session_id = cs.id
  )
) AS has_project
FROM chat_session cs
WHERE cs.id = sqlc.arg(id);

-- name: ViewerInChatProject :one
SELECT EXISTS (
  SELECT 1 FROM chat_session_project csp
  WHERE csp.chat_session_id = sqlc.arg(chat_session_id)
    AND csp.project_id = ANY(sqlc.arg(project_ids)::uuid[])
  UNION ALL
  SELECT 1 FROM chat_session cs
  WHERE cs.id = sqlc.arg(chat_session_id)
    AND cs.project_id IS NOT NULL
    AND cs.project_id = ANY(sqlc.arg(project_ids)::uuid[])
) AS in_project;

-- name: ListChatShareMemberAccess :many
SELECT member_id, access FROM resource_share
WHERE workspace_id = sqlc.arg(workspace_id)
  AND resource_type = 'chat_session'
  AND resource_id = sqlc.arg(resource_id);

-- name: ChatVisibilityNoticeDismissed :one
SELECT EXISTS (
  SELECT 1 FROM chat_visibility_notice
  WHERE workspace_id = sqlc.arg(workspace_id) AND user_id = sqlc.arg(user_id)
) AS dismissed;

-- name: DismissChatVisibilityNotice :exec
INSERT INTO chat_visibility_notice (workspace_id, user_id)
VALUES (sqlc.arg(workspace_id), sqlc.arg(user_id))
ON CONFLICT DO NOTHING;

-- name: ListOpenProjectChatsByCreator :many
SELECT cs.id, cs.title
FROM chat_session cs
WHERE cs.workspace_id = sqlc.arg(workspace_id)
  AND cs.creator_id = sqlc.arg(creator_id)
  AND cs.status = 'active'
  AND cs.visibility = 'project'
  AND (
    cs.project_id IS NOT NULL
    OR EXISTS (
      SELECT 1 FROM chat_session_project csp
      WHERE csp.chat_session_id = cs.id
    )
  )
ORDER BY cs.updated_at DESC;

-- name: ListChatDirectory :many
-- Read-only directory projection for agents. Visibility is evaluated against
-- the task's human originator (viewer_id), never the runtime owner. This query
-- deliberately does not touch chat_session_read, so browsing a directory or
-- reading its summaries cannot clear a person's unread state.
SELECT cs.id,
       cs.workspace_id,
       cs.agent_id,
       cs.creator_id,
       cs.title,
       cs.status,
       cs.visibility,
       cs.project_id,
       COALESCE(p.title, '') AS project_title,
       COALESCE(ag.name, '') AS agent_name,
       COALESCE(u.name, '') AS creator_name,
       COALESCE(lm.created_at, cs.updated_at) AS last_active_at,
       (SELECT count(*) FROM chat_message m
          WHERE m.chat_session_id = cs.id
            AND m.message_kind NOT IN ('channel_command', 'onboarding_kickoff'))::int AS message_count,
       COALESCE(lm.content, '') AS summary
FROM chat_session cs
LEFT JOIN project p ON p.id = cs.project_id AND p.workspace_id = cs.workspace_id
LEFT JOIN agent ag ON ag.id = cs.agent_id
LEFT JOIN "user" u ON u.id = cs.creator_id
LEFT JOIN LATERAL (
  SELECT content, created_at
    FROM chat_message m
   WHERE m.chat_session_id = cs.id
     AND m.message_kind NOT IN ('channel_command', 'onboarding_kickoff')
   ORDER BY m.created_at DESC, m.id DESC
   LIMIT 1
) lm ON true
WHERE cs.workspace_id = sqlc.arg(workspace_id)
  AND (
    cs.creator_id = sqlc.arg(viewer_id)
    OR (
      cs.visibility = 'workspace'
      AND EXISTS (
        SELECT 1 FROM member m
         WHERE m.workspace_id = cs.workspace_id
           AND m.user_id = sqlc.arg(viewer_id)
           AND m.role <> 'guest'
      )
    )
    OR (
      cs.visibility = 'project'
      AND (
        EXISTS (
          SELECT 1 FROM chat_session_project csp
           WHERE csp.chat_session_id = cs.id
             AND csp.project_id = ANY(sqlc.arg(project_ids)::uuid[])
        )
        OR (cs.project_id IS NOT NULL AND cs.project_id = ANY(sqlc.arg(project_ids)::uuid[]))
        OR EXISTS (
          SELECT 1 FROM resource_share rs
           WHERE rs.workspace_id = cs.workspace_id
             AND rs.resource_type = 'chat_session'
             AND rs.resource_id = cs.id::text
             AND rs.member_id = sqlc.arg(viewer_id)
        )
      )
    )
  )
  AND (sqlc.arg(all_projects)::bool OR (sqlc.narg(project_id)::uuid IS NOT NULL AND (
    cs.project_id = sqlc.narg(project_id)::uuid
    OR EXISTS (
      SELECT 1 FROM chat_session_project csp
       WHERE csp.chat_session_id = cs.id
         AND csp.project_id = sqlc.narg(project_id)::uuid
    )
  )))
  AND (sqlc.narg(since)::timestamptz IS NULL OR COALESCE(lm.created_at, cs.updated_at) >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(keyword)::text IS NULL OR cs.title ILIKE '%' || sqlc.narg(keyword)::text || '%'
       OR EXISTS (
         SELECT 1 FROM chat_message sm
          WHERE sm.chat_session_id = cs.id
            AND sm.message_kind NOT IN ('channel_command', 'onboarding_kickoff')
            AND sm.content ILIKE '%' || sqlc.narg(keyword)::text || '%'
       ))
  AND (cs.explicitly_created_at IS NOT NULL OR lm.created_at IS NOT NULL)
ORDER BY COALESCE(lm.created_at, cs.updated_at) DESC, cs.id DESC;
