-- The 'project' scope now means "specific people": the project's members, if
-- the issue has a project, plus the issue's own resource_share rows. An issue
-- with no project can therefore hold it, so the pairing rule from 510 goes.
-- Leaving or losing a project still demotes to 'private' in the handlers; that
-- only ever narrows.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_project_visibility_pairing;
