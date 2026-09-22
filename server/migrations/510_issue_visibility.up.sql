-- Resource-level sharing scope, part 2 of 3: issues (DENE-698).
--
-- The pairing constraint is 479_issue_view_project_visibility's: 'project'
-- scope names the people of the resource's project, so a resource with no
-- project cannot be set to it. The API rejects the same pairing with a 400 —
-- this constraint is the backstop, not the message.
--
-- Note for later migrations and for handler code: issue.project_id is
-- ON DELETE SET NULL (migration 034). Deleting a project would therefore break
-- this constraint for its project-scoped issues, so the delete path demotes
-- them to private first (see handler.demoteProjectScopedIssues).
ALTER TABLE issue
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'private';

ALTER TABLE issue
    DROP CONSTRAINT IF EXISTS issue_visibility_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_visibility_check
        CHECK (visibility IN ('private', 'project', 'workspace'));

UPDATE issue SET visibility = 'workspace';

ALTER TABLE issue
    DROP CONSTRAINT IF EXISTS issue_project_visibility_pairing;

ALTER TABLE issue
    ADD CONSTRAINT issue_project_visibility_pairing
        CHECK (visibility <> 'project' OR project_id IS NOT NULL);
