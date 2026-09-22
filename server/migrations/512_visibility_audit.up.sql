-- Every sharing change leaves one row here: who changed what, when, from which
-- scope to which, and how many people that scope reaches (DENE-698).
--
-- audience_size is a count taken at write time, not a live view: it answers
-- "how many people did this change expose the resource to" months later, when
-- the project's membership has moved on. resource_id is TEXT because a repo is
-- identified by its URL, not a UUID (see migration 511).
--
-- No foreign keys and no cascades, per repository policy: an audit row must
-- outlive the resource it describes.
CREATE TABLE IF NOT EXISTS visibility_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    actor_type TEXT NOT NULL CHECK (actor_type IN ('member', 'agent')),
    actor_id UUID NOT NULL,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('issue', 'project', 'repo')),
    resource_id TEXT NOT NULL,
    previous_visibility TEXT,
    new_visibility TEXT NOT NULL CHECK (new_visibility IN ('private', 'project', 'workspace')),
    audience_size INTEGER NOT NULL DEFAULT 0,
    -- 'direct': somebody set this one resource's scope.
    -- 'project_bulk': a project's scope change swept it along.
    source TEXT NOT NULL DEFAULT 'direct' CHECK (source IN ('direct', 'project_bulk')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
