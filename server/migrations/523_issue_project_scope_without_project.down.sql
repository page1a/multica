UPDATE issue SET visibility = 'private' WHERE visibility = 'project' AND project_id IS NULL;
ALTER TABLE issue
    ADD CONSTRAINT issue_project_visibility_pairing
        CHECK (visibility <> 'project' OR project_id IS NOT NULL);
