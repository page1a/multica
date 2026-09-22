-- Module-level sharing (DENE-699): a third AND gate on top of resource
-- visibility. Each workspace can restrict Issues, Projects, Repos and
-- Runtimes independently. Agents and squads are not rows here.
--
-- Missing row = workspace (every member, including guests, may enter).
-- Modules are not created the way an issue is, so defaulting them to
-- private would lock the product the moment this migration lands.
--
-- 'project' scope names one project's people, so it requires project_id.
-- No foreign keys, per repository policy.
CREATE TABLE IF NOT EXISTS workspace_module_visibility (
    workspace_id UUID NOT NULL,
    module TEXT NOT NULL CHECK (module IN ('issues', 'projects', 'repos', 'runtimes')),
    visibility TEXT NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('private', 'project', 'workspace')),
    project_id UUID,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, module),
    CHECK (visibility <> 'project' OR project_id IS NOT NULL)
);

-- Module changes reuse visibility_audit. resource_id is the module key
-- ('issues', ...). Existing issue/project/repo rows are untouched.
ALTER TABLE visibility_audit
    DROP CONSTRAINT IF EXISTS visibility_audit_resource_type_check;

ALTER TABLE visibility_audit
    ADD CONSTRAINT visibility_audit_resource_type_check
        CHECK (resource_type IN ('issue', 'project', 'repo', 'module'));
