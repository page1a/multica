-- The list/search predicate filters issues by workspace and scope on every
-- read; project_id rides along so the 'project' branch is answered from the
-- index too.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_workspace_visibility
    ON issue(workspace_id, visibility, project_id);
