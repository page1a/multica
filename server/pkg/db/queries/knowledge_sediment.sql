-- name: CreateKnowledgeSediment :one
INSERT INTO knowledge_sediment (
    workspace_id, project_id, issue_id, chat_session_id, changes, verified,
    mainline, commits, pr_url, author_type, author_id, layer, sources, memory_files
) VALUES (
    @workspace_id, sqlc.narg('project_id'), sqlc.narg('issue_id'), sqlc.narg('chat_session_id'),
    @changes, @verified, @mainline, @commits, @pr_url, @author_type, sqlc.narg('author_id'),
    @layer, @sources, @memory_files
)
RETURNING *;

-- name: ListProjectKnowledgeSediments :many
-- The project's newest sediment rows with what a reader needs to name the
-- source: the issue's number and title, or the chat's title.
SELECT ks.*,
       i.number AS issue_number,
       COALESCE(i.title, '')::text AS issue_title,
       COALESCE(cs.title, '')::text AS chat_title
FROM knowledge_sediment ks
LEFT JOIN issue i ON i.id = ks.issue_id
LEFT JOIN chat_session cs ON cs.id = ks.chat_session_id
WHERE ks.project_id = @project_id AND ks.workspace_id = @workspace_id
ORDER BY ks.created_at DESC
LIMIT @row_limit;

-- name: ListChatKnowledgeSediments :many
SELECT * FROM knowledge_sediment
WHERE chat_session_id = @chat_session_id AND workspace_id = @workspace_id
ORDER BY created_at DESC
LIMIT 20;

-- name: ListProjectKnowledgeSedimentsSince :many
-- The project monitor's writes (DENE-1681): every sediment in the window,
-- newest first, named like ListProjectKnowledgeSediments.
SELECT ks.*,
       i.number AS issue_number,
       COALESCE(i.title, '')::text AS issue_title,
       COALESCE(cs.title, '')::text AS chat_title
FROM knowledge_sediment ks
LEFT JOIN issue i ON i.id = ks.issue_id
LEFT JOIN chat_session cs ON cs.id = ks.chat_session_id
WHERE ks.project_id = @project_id AND ks.workspace_id = @workspace_id
  AND ks.created_at >= @since
ORDER BY ks.created_at DESC
LIMIT @row_limit;

-- name: ListProjectDoneIssuesWithoutSediment :many
-- The project monitor's unsettled closes (DENE-1681): tickets that finished
-- in the window without writing project memory. Sediment rounds are left out;
-- the monitor reports them as rounds.
SELECT i.* FROM issue i
WHERE i.workspace_id = @workspace_id AND i.project_id = @project_id
  AND i.status = 'done'
  AND i.updated_at >= @since
  AND NOT (i.metadata ? 'sediment_project')
  AND NOT EXISTS (SELECT 1 FROM knowledge_sediment ks WHERE ks.issue_id = i.id)
ORDER BY i.updated_at DESC, i.id DESC
LIMIT @row_limit;

-- name: ListProjectSedimentRounds :many
-- The project's sediment rounds opened in the window or still open, with
-- whether each wrote anything (DENE-1681).
SELECT sqlc.embed(i),
       EXISTS (SELECT 1 FROM knowledge_sediment ks WHERE ks.issue_id = i.id)::boolean AS wrote
FROM issue i
WHERE i.workspace_id = @workspace_id
  AND i.metadata @> jsonb_build_object('sediment_project', @project_id::text)
  AND (i.created_at >= @since OR i.status NOT IN ('done', 'cancelled'))
ORDER BY i.created_at DESC, i.id DESC
LIMIT @row_limit;
