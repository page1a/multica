-- DENE-1451: domains are workspace data, and projects, issues and
-- specialisations point at them by id.
--
-- Before this the domain list was hard-coded in ladder.json, a project's
-- domain was a row in workspace.settings.routing.projects keyed by the
-- project's NAME (a rename lost it), and a specialisation's domain was read off
-- the end of its name. Now:
--
--   workspace_domain     the one list a workspace maintains;
--   project.domain_ids   the domains a project carries, empty = generic;
--   issue.domain_id      the one domain an issue works in, NULL = generic;
--   agent.domain_id      the domain a specialisation covers, NULL = generic.
--
-- No foreign keys, as everywhere in this schema: the handler validates every
-- reference and clears them when a domain is deleted.
CREATE TABLE IF NOT EXISTS workspace_domain (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

ALTER TABLE project ADD COLUMN IF NOT EXISTS domain_ids UUID[] NOT NULL DEFAULT '{}';
ALTER TABLE issue ADD COLUMN IF NOT EXISTS domain_id UUID;
ALTER TABLE agent ADD COLUMN IF NOT EXISTS domain_id UUID;

-- 1. Every workspace starts with the directions ladder.json declared, plus
--    中转, which already had ten specialisations but was never a routing
--    direction. Any other value a workspace typed into its project table is
--    kept as a domain too, so no row loses its meaning below.
INSERT INTO workspace_domain (workspace_id, name, position)
SELECT w.id, d.name, d.position
FROM workspace w
CROSS JOIN (VALUES ('游戏', 0), ('出海', 1), ('自媒体', 2), ('中转', 3), ('学术', 4)) AS d(name, position)
ON CONFLICT (workspace_id, name) DO NOTHING;

INSERT INTO workspace_domain (workspace_id, name, position)
SELECT DISTINCT w.id, trim(r.value), 100
FROM workspace w,
     jsonb_each_text(w.settings -> 'routing' -> 'projects') AS r
WHERE jsonb_typeof(w.settings -> 'routing' -> 'projects') = 'object'
  AND trim(r.value) NOT IN ('', '通用')
ON CONFLICT (workspace_id, name) DO NOTHING;

-- 2. Specialisations take their domain from the end of their name
--    (孙悟空出海 -> 出海). The longest matching domain wins, and one base role
--    keeps at most one live specialisation per domain: a second match stays
--    generic rather than breaking that rule.
WITH candidates AS (
    SELECT a.id AS agent_id, a.parent_agent_id, d.id AS domain_id,
           row_number() OVER (PARTITION BY a.id ORDER BY length(d.name) DESC) AS by_length
    FROM agent a
    JOIN workspace_domain d ON d.workspace_id = a.workspace_id
    WHERE a.parent_agent_id IS NOT NULL
      AND a.domain_id IS NULL
      AND right(a.name, length(d.name)) = d.name
      AND length(a.name) > length(d.name)
), picked AS (
    SELECT c.agent_id, c.domain_id,
           row_number() OVER (
               PARTITION BY c.parent_agent_id, c.domain_id
               ORDER BY (a.archived_at IS NULL) DESC, a.created_at, a.id
           ) AS per_domain
    FROM candidates c
    JOIN agent a ON a.id = c.agent_id
    WHERE c.by_length = 1
)
UPDATE agent a
SET domain_id = p.domain_id
FROM picked p
WHERE a.id = p.agent_id
  AND p.per_domain = 1;

-- 3. Projects take their domain from the old name table: this workspace's own
--    rows over the rows ladder.json shipped, an exact name before the longest
--    `prefix*`, 通用 meaning no domain.
WITH workspace_rows AS (
    SELECT w.id AS workspace_id, lower(trim(r.key)) AS k, trim(r.value) AS v
    FROM workspace w,
         jsonb_each_text(w.settings -> 'routing' -> 'projects') AS r
    WHERE jsonb_typeof(w.settings -> 'routing' -> 'projects') = 'object'
), shipped_rows AS (
    SELECT w.id AS workspace_id, s.k, s.v
    FROM workspace w
    CROSS JOIN (VALUES ('game', '游戏'), ('game-multica', '游戏'), ('game-relay', '游戏')) AS s(k, v)
    WHERE NOT EXISTS (
        SELECT 1 FROM workspace_rows wr WHERE wr.workspace_id = w.id AND wr.k = s.k
    )
), all_rows AS (
    SELECT * FROM workspace_rows
    UNION ALL
    SELECT * FROM shipped_rows
), matched AS (
    SELECT DISTINCT ON (p.id) p.id AS project_id, p.workspace_id, r.v
    FROM project p
    JOIN all_rows r ON r.workspace_id = p.workspace_id
    WHERE lower(trim(p.title)) = r.k
       OR (right(r.k, 1) = '*'
           AND left(lower(trim(p.title)), length(r.k) - 1) = left(r.k, length(r.k) - 1))
    ORDER BY p.id, (lower(trim(p.title)) = r.k) DESC, length(r.k) DESC
)
UPDATE project p
SET domain_ids = ARRAY[d.id]
FROM matched m
JOIN workspace_domain d ON d.workspace_id = m.workspace_id AND d.name = m.v
WHERE p.id = m.project_id
  AND cardinality(p.domain_ids) = 0;

-- 4. A row naming one existing project is now that project's own field, so it
--    leaves the name table. Prefix rows and rows naming no project stay: they
--    are the fallback for a project nobody has set a domain on.
UPDATE workspace w
SET settings = jsonb_set(
    w.settings,
    '{routing,projects}',
    COALESCE((
        SELECT jsonb_object_agg(r.key, r.value)
        FROM jsonb_each(w.settings -> 'routing' -> 'projects') AS r
        WHERE NOT EXISTS (
            SELECT 1 FROM project p
            WHERE p.workspace_id = w.id
              AND lower(trim(p.title)) = lower(trim(r.key))
        )
    ), '{}'::jsonb)
)
WHERE jsonb_typeof(w.settings -> 'routing' -> 'projects') = 'object';

-- 5. An issue in a one-domain project works in that domain.
UPDATE issue i
SET domain_id = p.domain_ids[1]
FROM project p
WHERE i.project_id = p.id
  AND i.domain_id IS NULL
  AND cardinality(p.domain_ids) = 1;
