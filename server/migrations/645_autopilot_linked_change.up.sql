-- DENE-1663: an autopilot's own trail of writes made through a managed
-- workspace link — who started the run, from which viewer workspace, which
-- agent. Names are copied so the line still reads after a rename.
CREATE TABLE autopilot_linked_change (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    autopilot_id UUID NOT NULL REFERENCES autopilot(id) ON DELETE CASCADE,
    link_id UUID REFERENCES workspace_link(id) ON DELETE SET NULL,
    route TEXT NOT NULL,
    actor_id UUID NOT NULL,
    via_workspace_id UUID NOT NULL,
    via_workspace_name TEXT NOT NULL,
    via_slug TEXT NOT NULL,
    agent_id UUID NOT NULL,
    agent_name TEXT NOT NULL,
    task_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_autopilot_linked_change_autopilot
    ON autopilot_linked_change (autopilot_id, created_at DESC);
