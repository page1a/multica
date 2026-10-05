-- Issue state card (DENE-1328). The card is derived from records the issue
-- already keeps; only the "已拍板" list has its own table (issue_decision).

-- name: ListIssueDecisions :many
SELECT * FROM issue_decision
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
ORDER BY created_at ASC, id ASC;

-- name: CountIssueDecisions :one
SELECT count(*) FROM issue_decision
WHERE issue_id = @issue_id AND workspace_id = @workspace_id;

-- name: CreateIssueDecision :one
INSERT INTO issue_decision (workspace_id, issue_id, text, source, author_type, author_id)
VALUES (@workspace_id, @issue_id, @text, @source, @author_type, sqlc.narg('author_id')::uuid)
RETURNING *;

-- name: UpdateIssueDecisionText :one
UPDATE issue_decision
SET text = @text, updated_at = now()
WHERE id = @id AND issue_id = @issue_id AND workspace_id = @workspace_id
RETURNING *;

-- name: DeleteIssueDecision :one
DELETE FROM issue_decision
WHERE id = @id AND issue_id = @issue_id AND workspace_id = @workspace_id
RETURNING id;

-- name: GetLatestIssueProgressBySource :one
SELECT * FROM issue_progress
WHERE issue_id = @issue_id AND workspace_id = @workspace_id AND source = @source
ORDER BY created_at DESC
LIMIT 1;

-- name: GetAgentPreviousRunStartOnIssue :one
-- The caller's last run on this issue, other than the one asking: the anchor
-- "你上次之后的变化" is measured from. started_at, never completed_at — a long
-- run would otherwise miss comments posted while it ran (same rule as the
-- claim-time delta, CountNewCommentsSince).
SELECT started_at FROM agent_task_queue
WHERE issue_id = @issue_id
  AND agent_id = @agent_id
  AND id <> @exclude_task_id
  AND started_at IS NOT NULL
ORDER BY started_at DESC
LIMIT 1;

-- name: GetMemberLastCommentAtOnIssue :one
-- A person has no runs; their own last word on the issue is the anchor.
SELECT max(created_at)::timestamptz AS last_at FROM comment
WHERE issue_id = @issue_id AND workspace_id = @workspace_id
  AND author_type = 'member' AND author_id = @author_id
  AND deleted_at IS NULL;

-- name: ListIssueThreadsChangedSince :many
-- Threads with comments created strictly after @since by anyone but the
-- caller, newest activity first. Only the root is returned (its opening text
-- is the thread title); the count and timestamp cover the new comments.
-- A NULL @since lists every thread: the caller has never been here.
WITH RECURSIVE membership(id, root_id) AS (
    SELECT c.id, c.id
    FROM comment c
    WHERE c.issue_id = @issue_id AND c.workspace_id = @workspace_id AND c.parent_id IS NULL
    UNION ALL
    SELECT c.id, m.root_id
    FROM comment c
    JOIN membership m ON c.parent_id = m.id
    WHERE c.issue_id = @issue_id AND c.workspace_id = @workspace_id
),
fresh AS (
    SELECT m.root_id, count(*)::int AS new_count, max(c.created_at)::timestamptz AS last_at
    FROM membership m
    JOIN comment c ON c.id = m.id
    WHERE c.deleted_at IS NULL
      AND (sqlc.narg('since')::timestamptz IS NULL OR c.created_at > sqlc.narg('since')::timestamptz)
      AND NOT (c.author_type = @caller_type::text AND c.author_id IS NOT DISTINCT FROM sqlc.narg('caller_id')::uuid)
    GROUP BY m.root_id
)
SELECT r.id, r.author_type, r.author_id, r.content, r.type, r.created_at, r.deleted_at,
       f.new_count, f.last_at
FROM fresh f
JOIN comment r ON r.id = f.root_id
ORDER BY f.last_at DESC, r.id DESC
LIMIT @row_limit;
