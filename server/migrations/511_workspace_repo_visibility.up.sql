-- Resource-level sharing scope, part 3 of 3: repositories (DENE-698).
--
-- A workspace's repositories are entries in workspace.repos, a JSONB array of
-- {url, description} — there is no repo table to add a column to, so the scope
-- lives on the entry: {url, description, visibility, created_by}.
--
-- Which project a repo belongs to is not stored on the entry either. It is the
-- set of project_resource rows of type github_repo whose resource_ref->>'url'
-- matches, which is the same link the project page already shows. The pairing
-- rule ("no project, no 'project' scope") is therefore enforced in the handler
-- against that table; a JSONB column cannot express it as a CHECK.
--
-- Existing entries are backfilled to workspace, matching 503 and 504. They get
-- no created_by: nothing recorded who added them, and a creator only matters
-- for a private entry, which a backfilled one never is.
UPDATE workspace
SET repos = COALESCE((
        SELECT jsonb_agg(entry || jsonb_build_object('visibility', 'workspace') ORDER BY ordinality)
        FROM jsonb_array_elements(repos) WITH ORDINALITY AS t(entry, ordinality)
        WHERE jsonb_typeof(entry) = 'object'
    ), '[]'::jsonb)
WHERE jsonb_typeof(repos) = 'array'
  AND jsonb_array_length(repos) > 0;
