-- Cross-workspace read-only links (DENE-1225). A source workspace's owner
-- offers a viewer workspace a read-only window onto chosen projects; the
-- viewer's owner/admin accepts it. Nobody joins the source's member table.
-- All rules live in server/internal/workspacelink; see docs/adr/0004.
CREATE TABLE workspace_link (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    target_workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active')),
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    accepted_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at TIMESTAMPTZ,
    UNIQUE (source_workspace_id, target_workspace_id),
    CHECK (source_workspace_id <> target_workspace_id)
);

CREATE INDEX idx_workspace_link_target ON workspace_link(target_workspace_id);

-- The projects a link exposes. Deleting the project drops it from every link.
CREATE TABLE workspace_link_project (
    link_id UUID NOT NULL REFERENCES workspace_link(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    PRIMARY KEY (link_id, project_id)
);

-- Who did what to a link. Outlives the link (revoke deletes the link row) so
-- both sides' owners can still read it; goes when either workspace goes.
CREATE TABLE workspace_link_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id UUID NOT NULL,
    source_workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    target_workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL,
    actor_id UUID,
    action TEXT NOT NULL CHECK (action IN ('create', 'update_projects', 'accept', 'revoke')),
    detail JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_link_audit_source ON workspace_link_audit(source_workspace_id, created_at DESC);
CREATE INDEX idx_workspace_link_audit_target ON workspace_link_audit(target_workspace_id, created_at DESC);
