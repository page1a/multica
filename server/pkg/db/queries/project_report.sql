-- "听汇报" (DENE-1667): a project's news since a person last heard it.

-- name: GetProjectReportCursor :one
-- The person's newest heard row for a project: heard_until is where the next
-- report starts.
SELECT * FROM project_report_heard
WHERE workspace_id = $1 AND user_id = $2 AND project_id = $3
ORDER BY heard_until DESC, created_at DESC
LIMIT 1;

-- name: InsertProjectReportHeard :one
INSERT INTO project_report_heard (
    workspace_id, user_id, project_id, heard_since, heard_until,
    item_count, task_id, chat_session_id, actions
) VALUES (
    $1, $2, $3, $4, $5, $6,
    sqlc.narg('task_id'), sqlc.narg('chat_session_id'),
    COALESCE(sqlc.narg('actions')::jsonb, '[]'::jsonb)
)
RETURNING *;

-- name: ListProjectReportHeardByTask :many
-- Reports one chat run delivered, oldest first. The turn's follow-up buttons
-- are built from their actions.
SELECT * FROM project_report_heard
WHERE task_id = $1
ORDER BY created_at ASC, id ASC;

-- name: ListProjectReportStatusChanges :many
-- Every status move of the project's issues inside (since, until], oldest
-- first. The report folds them into one "A → B" per issue.
SELECT a.issue_id,
       COALESCE(a.details->>'from', '')::text AS from_status,
       COALESCE(a.details->>'to', '')::text AS to_status,
       a.created_at
FROM activity_log a
JOIN issue i ON i.id = a.issue_id
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND i.project_id = sqlc.arg('project_id')::uuid
  AND a.action = 'status_changed'
  AND a.created_at > sqlc.arg('since')::timestamptz
  AND a.created_at <= sqlc.arg('until')::timestamptz
ORDER BY a.created_at ASC, a.id ASC;

-- name: ListProjectReportCreatedIssues :many
-- Issues opened in the project inside (since, until]: news even before their
-- first status move.
SELECT id, created_at FROM issue
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND project_id = sqlc.arg('project_id')::uuid
  AND created_at > sqlc.arg('since')::timestamptz
  AND created_at <= sqlc.arg('until')::timestamptz
ORDER BY created_at ASC;

-- name: ListProjectReportIssues :many
-- The report's issues with what the report says about each: the chat that
-- opened it (DENE-1665's origin_chat_session_id, else the chat run behind an
-- agent's `issue create` from before that column) and whether an unread call
-- to the person hangs on it.
SELECT i.id, i.number, i.title, i.status, i.priority, i.description,
       i.assignee_type, i.assignee_id, i.visibility, i.creator_type, i.creator_id,
       i.project_id, i.updated_at, i.metadata,
       COALESCE(i.origin_chat_session_id, t.chat_session_id)::uuid AS source_chat_id,
       EXISTS (
           SELECT 1 FROM inbox_item n
           WHERE n.workspace_id = i.workspace_id
             AND n.recipient_type = 'member'
             AND n.recipient_id = sqlc.arg('user_id')::uuid
             AND n.issue_id = i.id
             AND n.severity = 'action_required'
             AND n.read = false AND n.archived = false
       )::boolean AS has_open_call
FROM issue i
LEFT JOIN agent_task_queue t
       ON i.origin_type = 'agent_create' AND t.id = i.origin_id
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND i.id = ANY(sqlc.arg('issue_ids')::uuid[]);

-- name: MarkProjectReportInboxRead :many
-- Hearing a report reads the person's notifications on the issues it covered,
-- up to the moment it covers — the same open-call exception as
-- MarkIssueInboxRead (keep the predicate in step).
UPDATE inbox_item i SET read = true, read_at = now()
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = sqlc.arg('user_id')::uuid
  AND i.issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
  AND i.created_at <= sqlc.arg('until')::timestamptz
  AND i.archived = false AND i.read = false
  AND NOT EXISTS (
      SELECT 1 FROM issue_summon s
      JOIN issue siss ON siss.id = s.issue_id
      WHERE s.issue_id = i.issue_id
        AND s.recipient_id = i.recipient_id
        AND s.answered_at IS NULL
        AND siss.status NOT IN ('done', 'cancelled')
        AND (s.inbox_item_id = i.id
             OR (s.comment_id IS NOT NULL AND i.details->>'comment_id' = s.comment_id::text))
  )
RETURNING i.id;

-- name: ListChatTicketProgress :many
-- The chat's progress bar (DENE-1667): for each ticket the chat opened, its
-- latest status move and whether an unread call to the person hangs on it —
-- the same call predicate as ListProjectReportIssues.
SELECT i.id,
       COALESCE(m.from_status, '')::text AS from_status,
       m.changed_at::timestamptz AS changed_at,
       EXISTS (
           SELECT 1 FROM inbox_item n
           WHERE n.workspace_id = i.workspace_id
             AND n.recipient_type = 'member'
             AND n.recipient_id = sqlc.arg('user_id')::uuid
             AND n.issue_id = i.id
             AND n.severity = 'action_required'
             AND n.read = false AND n.archived = false
       )::boolean AS has_open_call
FROM issue i
LEFT JOIN LATERAL (
    SELECT a.details->>'from' AS from_status, a.created_at AS changed_at
    FROM activity_log a
    WHERE a.issue_id = i.id AND a.action = 'status_changed'
    ORDER BY a.created_at DESC, a.id DESC
    LIMIT 1
) m ON true
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND i.id = ANY(sqlc.arg('issue_ids')::uuid[]);
