-- Safe rollback: never widen (migration 479's down is the precedent). Every
-- shared row is narrowed to private before the column goes, so no instant of
-- the rollback shows a project to more people than the instant before it.
--
-- Once the column is gone the reading code is gone with it, and the server is
-- back to its pre-DENE-698 behaviour where every project is visible to every
-- member. That is the rollback target, not a leak this migration introduces —
-- but every scope change made while the feature was live is recorded in
-- visibility_audit (migration 512), so the narrowing can be replayed after a
-- roll forward.
UPDATE project SET visibility = 'private' WHERE visibility <> 'private';

ALTER TABLE project
    DROP CONSTRAINT IF EXISTS project_visibility_check;

ALTER TABLE project
    DROP COLUMN IF EXISTS visibility;

ALTER TABLE project
    DROP COLUMN IF EXISTS created_by;
