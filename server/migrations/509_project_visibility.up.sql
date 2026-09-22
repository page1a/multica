-- Resource-level sharing scope, part 1 of 3: projects (DENE-698).
--
-- The vocabulary is issue_view's (migrations 265, 479) so the whole product
-- speaks one set of scope names: private / project / workspace. New rows are
-- private — zero trust, see docs/kun/permission-model.md — and the rows that
-- already exist are backfilled to workspace so this migration takes sight of
-- nothing away from anybody.
--
-- A project is its own project, so 'project' scope always names a real set of
-- people here and no pairing constraint is needed. Issues and repos, which may
-- belong to no project at all, get one (see 504, 505).
ALTER TABLE project
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'private';

ALTER TABLE project
    DROP CONSTRAINT IF EXISTS project_visibility_check;

ALTER TABLE project
    ADD CONSTRAINT project_visibility_check
        CHECK (visibility IN ('private', 'project', 'workspace'));

-- created_by is part of the same feature, not a drive-by: 'private' means
-- "only the creator", and until now nothing recorded who created a project.
-- Without it a private project would be visible to nobody, including the
-- person who just made it. No FK, per repository policy — a departed member's
-- id stays readable as an id.
ALTER TABLE project
    ADD COLUMN IF NOT EXISTS created_by UUID;

-- Existing projects get their member lead as the nearest available truth about
-- who owns them. Agent-led and unled projects stay NULL, which is honest: the
-- backfill below makes them workspace-visible anyway, so no creator is needed
-- to see them.
UPDATE project
SET created_by = lead_id
WHERE created_by IS NULL AND lead_type = 'member' AND lead_id IS NOT NULL;

UPDATE project SET visibility = 'workspace';
