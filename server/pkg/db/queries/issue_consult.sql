-- name: LockIssueConsults :exec
-- Serializes the count-then-insert of one ticket's consults, so two runs
-- asking at once cannot both pass the limit on a stale count.
SELECT pg_advisory_xact_lock(hashtext('issue_consult'), hashtext(sqlc.arg('issue_id')::text));

-- name: CountIssueConsultsAgainstLimit :one
-- Pending and answered consults use up the ticket's allowance; a failed one
-- never reached an answer and gives its slot back.
SELECT count(*) FROM issue_consult
WHERE issue_id = $1 AND status IN ('pending', 'answered');

-- name: CreateIssueConsult :one
INSERT INTO issue_consult (
    id, workspace_id, issue_id, asker_agent_id, asker_task_id, advisor_agent_id, question
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: SetIssueConsultTask :exec
UPDATE issue_consult SET advisor_task_id = $2 WHERE id = $1;

-- name: GetIssueConsult :one
SELECT * FROM issue_consult WHERE id = $1 AND workspace_id = $2;

-- name: ListIssueConsults :many
SELECT * FROM issue_consult WHERE issue_id = $1 ORDER BY created_at;

-- name: FinishIssueConsult :one
-- Only a pending consult finishes, and only once: a retried advisor run that
-- completes after the first answer landed changes nothing.
UPDATE issue_consult
SET status = sqlc.arg(status),
    answer = sqlc.narg(answer),
    failure_reason = sqlc.narg(failure_reason),
    tokens_used = sqlc.arg(tokens_used),
    duration_seconds = sqlc.arg(duration_seconds),
    finished_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;
