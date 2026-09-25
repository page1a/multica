-- Agent borrowing: doorbell requests and time-limited passes (DENE-808).
-- See migration 524.

-- name: ListActiveAgentAccessPassesForUser :many
-- Every unexpired, unrevoked pass a user holds in a workspace. Used by the
-- invoke gate (one agent) and the list/visibility filters (batch by agent).
SELECT * FROM agent_access_pass
WHERE workspace_id = $1
  AND user_id = $2
  AND revoked_at IS NULL
  AND expires_at > now()
ORDER BY expires_at ASC;

-- name: HasActiveAgentAccessPass :one
SELECT EXISTS (
    SELECT 1 FROM agent_access_pass
    WHERE agent_id = $1
      AND user_id = $2
      AND revoked_at IS NULL
      AND expires_at > now()
);

-- name: ListAgentAccessPasses :many
-- Owner's view of an agent's passes: active ones first, then the recent
-- history so a revoke / expiry is still visible for a while.
SELECT p.*,
       u.name AS user_name,
       u.email AS user_email,
       u.avatar_url AS user_avatar_url
FROM agent_access_pass p
JOIN "user" u ON u.id = p.user_id
WHERE p.agent_id = $1
  AND (p.revoked_at IS NULL AND p.expires_at > now()
       OR p.created_at > now() - interval '7 days')
ORDER BY (p.revoked_at IS NULL AND p.expires_at > now()) DESC, p.expires_at DESC;

-- name: CreateAgentAccessPass :one
INSERT INTO agent_access_pass (
    workspace_id, agent_id, user_id, granted_by, expires_at, request_id
) VALUES ($1, $2, $3, $4, $5, sqlc.narg('request_id'))
RETURNING *;

-- name: GetAgentAccessPass :one
SELECT * FROM agent_access_pass WHERE id = $1;

-- name: RevokeAgentAccessPass :one
UPDATE agent_access_pass
SET revoked_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: CreateAgentAccessRequest :one
-- ON CONFLICT on the one-pending index: a repeat ring on the same issue
-- returns the existing pending row (its summary refreshed) so the caller
-- can tell the requester "still waiting" without paging the owner twice.
INSERT INTO agent_access_request (
    workspace_id, agent_id, requester_id, issue_id, comment_id,
    trigger_kind, summary, expires_at
) VALUES (
    @workspace_id, @agent_id, @requester_id,
    sqlc.narg('issue_id')::uuid, sqlc.narg('comment_id')::uuid,
    @trigger_kind, @summary, @expires_at
)
ON CONFLICT (agent_id, requester_id, issue_id) WHERE status = 'pending'
DO UPDATE SET summary = EXCLUDED.summary
RETURNING *, (xmax = 0) AS inserted;

-- name: GetAgentAccessRequest :one
SELECT * FROM agent_access_request WHERE id = $1;

-- name: GetPendingAgentAccessRequest :one
SELECT * FROM agent_access_request
WHERE agent_id = $1 AND requester_id = $2 AND issue_id = $3 AND status = 'pending';

-- name: ResolveAgentAccessRequest :one
-- Compare-and-set from pending: two owners (or a double click) cannot both
-- approve, and an expired row cannot be approved after its deadline.
UPDATE agent_access_request
SET status = $2, resolved_by = $3, resolved_at = now()
WHERE id = $1 AND status = 'pending' AND expires_at > now()
RETURNING *;

-- name: ExpireAgentAccessRequests :many
UPDATE agent_access_request
SET status = 'expired', resolved_at = now()
WHERE status = 'pending' AND expires_at <= now()
RETURNING *;

-- name: ListAgentAccessRequestsForOwner :many
-- Rings on agents the user owns: pending first, then recent history.
SELECT r.*,
       a.name AS agent_name,
       u.name AS requester_name,
       u.email AS requester_email,
       u.avatar_url AS requester_avatar_url,
       i.number AS issue_number,
       i.title AS issue_title
FROM agent_access_request r
JOIN agent a ON a.id = r.agent_id
JOIN "user" u ON u.id = r.requester_id
LEFT JOIN issue i ON i.id = r.issue_id
WHERE r.workspace_id = $1
  AND a.owner_id = $2
  AND (r.status = 'pending' OR r.created_at > now() - interval '7 days')
ORDER BY (r.status = 'pending') DESC, r.created_at DESC;

-- name: ListAgentAccessRequestsForRequester :many
SELECT r.*,
       a.name AS agent_name,
       u.name AS requester_name,
       u.email AS requester_email,
       u.avatar_url AS requester_avatar_url,
       i.number AS issue_number,
       i.title AS issue_title
FROM agent_access_request r
JOIN agent a ON a.id = r.agent_id
JOIN "user" u ON u.id = r.requester_id
LEFT JOIN issue i ON i.id = r.issue_id
WHERE r.workspace_id = $1
  AND r.requester_id = $2
  AND (r.status = 'pending' OR r.created_at > now() - interval '7 days')
ORDER BY (r.status = 'pending') DESC, r.created_at DESC;
