-- Direct shares: the people a single issue or repository is shared with, on
-- top of its project's members. Under the 'project' scope (shown as
-- "指定的人" / "Specific people") a resource reaches its project's members plus
-- every row here; at any other scope these rows grant nothing. This is what
-- lets a resource with no project be shared with one named person.
--
-- resource_id is text because a repository has no row of its own: it is an
-- entry in workspace.repos keyed by URL. An issue stores its id as text.
-- No FKs or cascades by repository policy — cleanup runs in application
-- transactions (issue delete, repo removal, member removal). Indexes land in
-- 521–522 as concurrent single-statement builds.
CREATE TABLE resource_share (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('issue', 'repo')),
    resource_id TEXT NOT NULL,
    -- workspace member's user_id, same id space as project_member.member_id
    member_id UUID NOT NULL,
    added_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
