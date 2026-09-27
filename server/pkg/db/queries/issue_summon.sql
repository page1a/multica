-- name: CreateIssueSummon :one
-- ON CONFLICT against the open-row unique index is the dedupe: a second call
-- to the same person on the same ticket while the first is unanswered
-- returns no row, and the caller reports it as a duplicate.
INSERT INTO issue_summon (
    workspace_id, issue_id, recipient_id, caller_type, caller_id, source, reason, comment_id
) VALUES (
    $1, $2, $3, $4, sqlc.narg('caller_id'), $5, $6, sqlc.narg('comment_id')
)
ON CONFLICT (issue_id, recipient_id) WHERE answered_at IS NULL DO NOTHING
RETURNING *;

-- name: GetOpenIssueSummon :one
SELECT * FROM issue_summon
WHERE issue_id = $1 AND recipient_id = $2 AND answered_at IS NULL;

-- name: SetIssueSummonDelivery :exec
UPDATE issue_summon
SET comment_id = COALESCE(sqlc.narg('comment_id'), comment_id),
    inbox_item_id = COALESCE(sqlc.narg('inbox_item_id'), inbox_item_id)
WHERE id = $1;

-- name: AnswerIssueSummons :many
-- The person's comment on the ticket answers every open call to them there.
UPDATE issue_summon
SET answered_at = now(), answer_comment_id = $3
WHERE issue_id = $1 AND recipient_id = $2 AND answered_at IS NULL
RETURNING *;

-- name: ClaimIssueSummonReminder :one
-- One reminder per answered call: the run the answer woke ended with the
-- ticket still blocked, and the executor is told once to close it again.
UPDATE issue_summon
SET reminded_at = now()
WHERE answer_comment_id = $1 AND reminded_at IS NULL
RETURNING *;

-- name: ListOpenIssueSummonsForRecipient :many
-- The "等你" list (DENE-880 → stage 2): calls to this person still waiting on
-- their reply, on tickets that are not finished. One row per ticket (the
-- open-row index allows one per ticket and person), newest first.
SELECT s.id, s.issue_id, s.caller_type, s.caller_id, s.source, s.reason,
       s.comment_id, s.inbox_item_id, s.created_at,
       iss.number AS issue_number, iss.title AS issue_title,
       iss.status AS issue_status, iss.priority AS issue_priority,
       COALESCE(iss.assignee_type, '')::text AS issue_assignee_type,
       iss.assignee_id AS issue_assignee_id,
       COALESCE(iss.visibility, 'workspace')::text AS issue_visibility,
       COALESCE(iss.creator_type, '')::text AS issue_creator_type,
       iss.creator_id AS issue_creator_id,
       iss.project_id AS issue_project_id,
       COALESCE(u.name, a.name, '')::text AS caller_name
FROM issue_summon s
JOIN issue iss ON iss.id = s.issue_id
LEFT JOIN "user" u ON s.caller_type = 'member' AND u.id = s.caller_id
LEFT JOIN agent a ON s.caller_type = 'agent' AND a.id = s.caller_id
WHERE s.workspace_id = $1 AND s.recipient_id = $2 AND s.answered_at IS NULL
  AND iss.status NOT IN ('done', 'cancelled')
ORDER BY s.created_at DESC;

-- name: CloseOpenIssueSummons :many
-- The ticket moved on without a reply (DENE-901): it finished, or the person
-- called changed its status or owner themselves. The call is over; it is
-- closed the way an answer closes it, minus the answer comment, so no
-- reminder ever keys on it. A NULL recipient closes every call on the ticket.
UPDATE issue_summon
SET answered_at = now()
WHERE issue_id = $1 AND answered_at IS NULL
  AND (sqlc.narg('recipient_id')::uuid IS NULL OR recipient_id = sqlc.narg('recipient_id')::uuid)
RETURNING *;
