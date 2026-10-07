-- Workspace domains (DENE-1451). One list per workspace; projects, issues and
-- specialisations point at its rows by id.

-- name: ListWorkspaceDomains :many
SELECT * FROM workspace_domain
WHERE workspace_id = $1
ORDER BY position, created_at, name;

-- name: GetWorkspaceDomain :one
SELECT * FROM workspace_domain
WHERE id = $1 AND workspace_id = $2;

-- name: CreateWorkspaceDomain :one
INSERT INTO workspace_domain (workspace_id, name, position)
VALUES (
    $1, $2,
    COALESCE((SELECT max(position) + 1 FROM workspace_domain WHERE workspace_id = $1), 0)
)
RETURNING *;

-- name: RenameWorkspaceDomain :one
UPDATE workspace_domain
SET name = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteWorkspaceDomain :execrows
DELETE FROM workspace_domain
WHERE id = $1 AND workspace_id = $2;

-- name: CountWorkspaceDomainUsage :many
-- How many projects and live specialisations carry each domain. The settings
-- list shows it, and delete is refused while either is non-zero.
SELECT d.id,
       (SELECT count(*) FROM project p
          WHERE p.workspace_id = d.workspace_id AND d.id = ANY(p.domain_ids))::bigint AS project_count,
       (SELECT count(*) FROM agent a
          WHERE a.workspace_id = d.workspace_id AND a.domain_id = d.id AND a.archived_at IS NULL)::bigint AS agent_count
FROM workspace_domain d
WHERE d.workspace_id = $1;

-- name: ClearIssueDomain :execrows
-- Deleting a domain releases every issue still pointing at it.
UPDATE issue SET domain_id = NULL
WHERE workspace_id = $1 AND domain_id = $2;

-- name: ClearArchivedAgentDomain :execrows
UPDATE agent SET domain_id = NULL
WHERE workspace_id = $1 AND domain_id = $2 AND archived_at IS NOT NULL;

-- name: SetProjectDomains :one
UPDATE project SET domain_ids = sqlc.arg('domain_ids')::uuid[], updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ClearProjectIssueDomainsOutside :execrows
-- A project that drops a domain drops it from its issues too: an issue may
-- only work in a domain its project carries.
UPDATE issue SET domain_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND project_id = $2
  AND domain_id IS NOT NULL
  AND NOT (domain_id = ANY(sqlc.arg('domain_ids')::uuid[]));

-- name: FillProjectIssueDomain :execrows
-- A project that now carries exactly one domain gives it to its issues that
-- have none.
UPDATE issue SET domain_id = sqlc.arg('domain_id')::uuid, updated_at = now()
WHERE workspace_id = $1 AND project_id = $2 AND domain_id IS NULL;

-- name: SetIssueDomain :one
UPDATE issue SET domain_id = sqlc.narg('domain_id')::uuid, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetAgentDomain :one
UPDATE agent SET domain_id = sqlc.narg('domain_id')::uuid, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: GetSpecializationByDomain :one
-- The live specialisation of a base role for one domain, if there is one.
SELECT * FROM agent
WHERE parent_agent_id = $1 AND domain_id = $2 AND archived_at IS NULL
LIMIT 1;

-- name: ListSpecializationsByDomain :many
-- Specialisations named after a domain; a rename renames them with it.
SELECT a.* FROM agent a
JOIN agent p ON p.id = a.parent_agent_id
WHERE a.workspace_id = $1 AND a.domain_id = $2
  AND a.name = p.name || sqlc.arg('old_name')::text;

-- name: RenameAgentForDomain :one
UPDATE agent SET name = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SeedWorkspaceDomains :exec
-- A new workspace starts with the default list (ladder.json directions), in
-- that order. Existing names are left alone.
INSERT INTO workspace_domain (workspace_id, name, position)
SELECT @workspace_id::uuid, d.name, (d.ord - 1)::int
FROM unnest(@names::text[]) WITH ORDINALITY AS d(name, ord)
ON CONFLICT (workspace_id, name) DO NOTHING;
