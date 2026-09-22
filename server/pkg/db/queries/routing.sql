-- Queries for the routing layer (DENE-633).
--
-- Every write here is conditional or idempotent by construction. The routing
-- rule is "only fill empty slots, never change a value that is already there",
-- and issue creation plus the status change that follows it can call the
-- router twice almost simultaneously — so the emptiness test has to be part of
-- the write, not a read that precedes it.

-- name: AssignIssueIfUnassigned :one
-- Fills the executor slot only while it is still empty. Returns no row when
-- somebody (a person, an earlier routing call, or the concurrent one) already
-- put an assignee there, which is how the caller learns it must not report an
-- assignment it did not make.
UPDATE issue
SET assignee_type = sqlc.arg('assignee_type')::text,
    assignee_id = sqlc.arg('assignee_id')::uuid,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND assignee_id IS NULL
RETURNING *;

-- name: SetIssueReviewerIfUnset :one
-- Fills the reviewer slot only while it is still empty. reviewer_type IS NULL
-- is the empty slot; 'none' ("needs no acceptance pass") is a written value
-- and blocks this write exactly like a named reviewer does. Returns no row
-- when the slot already held an answer.
UPDATE issue
SET reviewer_type = sqlc.arg('reviewer_type')::text,
    reviewer_id = sqlc.narg('reviewer_id')::uuid,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND reviewer_type IS NULL
RETURNING *;

-- name: ClearIssueReviewer :execrows
-- Drops a reviewer reference that no longer points at anybody. The reviewer
-- pair is a reference, not a copy of a name, so the one thing it needs from
-- the rest of the server is to be released when its target is archived — the
-- same cleanup the no-foreign-keys rule requires for every other reference.
UPDATE issue
SET reviewer_type = NULL,
    reviewer_id = NULL,
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')::uuid
  AND reviewer_type = sqlc.arg('reviewer_type')::text
  AND reviewer_id = sqlc.arg('reviewer_id')::uuid;

-- name: SetIssuePropertyValueIfUnset :one
-- Fills one property slot only while it is still empty. Mirrors
-- SetIssuePropertyValue, with the `properties ? key` guard moved into the
-- WHERE clause. Returns no row when the slot already held a value.
UPDATE issue
SET properties = jsonb_set(properties, ARRAY[sqlc.arg('key')::text], sqlc.arg('value')::jsonb, true),
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND NOT (properties ? sqlc.arg('key')::text)
RETURNING *;

-- name: ReassignIssue :one
-- The in-review handoff. Unlike the two above this is not a fill: it moves a
-- ticket that already has an assignee to whoever accepts it. It is still
-- guarded — by the one-comment-per-kind index on the handoff comment — so a
-- status flipped back and forth cannot reassign twice.
UPDATE issue
SET assignee_type = sqlc.arg('assignee_type')::text,
    assignee_id = sqlc.arg('assignee_id')::uuid,
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
RETURNING *;

-- name: CreateRoutingComment :one
-- Posts one routing comment of a given kind. ON CONFLICT DO NOTHING against
-- comment_routing_kind_uniq means the second of two concurrent calls writes
-- nothing and returns no row, rather than leaving a duplicate on the ticket.
INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, routing_kind)
VALUES (
    sqlc.arg('issue_id')::uuid,
    sqlc.arg('workspace_id')::uuid,
    'system',
    sqlc.arg('author_id')::uuid,
    sqlc.arg('content')::text,
    'system',
    sqlc.arg('routing_kind')::text
)
ON CONFLICT (issue_id, routing_kind) WHERE routing_kind IS NOT NULL DO NOTHING
RETURNING *;

-- name: HasRoutingComment :one
SELECT EXISTS (
    SELECT 1 FROM comment
    WHERE issue_id = sqlc.arg('issue_id')::uuid
      AND routing_kind = sqlc.arg('routing_kind')::text
)::bool;

-- name: ListRoutingEnabledWorkspaces :many
-- Every workspace whose routing switch is on. The stale-review sweep has no
-- request to hang off and no workspace to be told about, so it starts here;
-- the flag is read again through the settings parser before anything is
-- written, and this query is only the cheap way to skip the rest.
SELECT id FROM workspace
WHERE settings -> 'routing' ->> 'enabled' = 'true'
ORDER BY id;

-- name: ListStaleReviewIssues :many
-- Tickets awaiting acceptance that nothing has happened to, and that no run is
-- working on right now.
--
-- The quiet clock is last_activity_at, not "when it entered review": the row
-- exists for tickets nobody will move again, and a ticket commented on an hour
-- ago is not one of them whatever its entry time. A ticket with no
-- last_activity_at falls back to updated_at rather than counting as infinitely
-- stale.
--
-- The NOT EXISTS is the other half of "stalled": a queued or running task means
-- the seat is going to speak, and waking it would be a second dispatcher.
SELECT i.id FROM issue i
WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
  AND i.status = ANY(sqlc.arg('statuses')::text[])
  AND COALESCE(i.last_activity_at, i.updated_at) < sqlc.arg('before')::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue q
      WHERE q.issue_id = i.id
        AND q.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
  )
ORDER BY COALESCE(i.last_activity_at, i.updated_at) ASC
LIMIT sqlc.arg('lim')::int;

-- name: LastEnteredReviewAt :one
-- When this ticket last ENTERED the awaiting-acceptance category.
--
-- It bounds the completion gate to the current review round. A ticket can go
-- through review more than once — reviewer passes it, a person sends it back,
-- the executor redoes the work, it returns to in_review — and the pass verdict
-- from the first round is still sitting on the thread. Without this boundary
-- the stale sweep would read that stale verdict as acceptance of work nobody
-- has looked at, which is exactly the thing this package must never do: align
-- to a fact, not to an expired one.
--
-- No row means the entry moment is unknown (the activity row is written by a
-- best-effort bus listener, and tickets that entered review before that
-- listener existed have none). Callers read that as "no verdict in this
-- round", which routes to the wake — the safe direction.
SELECT created_at FROM activity_log
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND action = 'status_changed'
  AND details->>'to' = ANY(sqlc.arg('statuses')::text[])
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListReviewerCommentsForIssue :many
-- What the reviewer themselves said on this ticket IN THE CURRENT REVIEW
-- ROUND, oldest first. It is the deterministic half of the completion gate: no
-- remark from this author since the ticket last entered review means there is
-- no acceptance for a status to be aligned to, whatever a model answers.
-- Deleted comments are excluded — a retracted verdict is not a verdict.
SELECT content FROM comment
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND author_type = sqlc.arg('author_type')::text
  AND author_id = sqlc.arg('author_id')::uuid
  AND created_at >= sqlc.arg('since')::timestamptz
  AND deleted_at IS NULL
ORDER BY created_at ASC;

-- name: CompleteIssueFromReview :one
-- The one status write this package performs, and the only conditional write
-- here whose guard is a status rather than an empty slot. Returning no row
-- means the ticket left the in-review category between the decision and the
-- write — somebody else moved it, and their answer wins.
UPDATE issue
SET status = 'done',
    revision = revision + 1,
    last_activity_at = GREATEST(COALESCE(last_activity_at, updated_at), now()),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid
  AND workspace_id = sqlc.arg('workspace_id')::uuid
  AND status = ANY(sqlc.arg('statuses')::text[])
RETURNING *;
