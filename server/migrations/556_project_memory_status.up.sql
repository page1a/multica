-- The daemon's last read-only project-memory check.  This is deliberately a
-- separate, replaceable projection: workspace settings hold only the static
-- sediment-seat preference, and issue metadata holds only the sediment round
-- correlation keys.
--
-- Renumbered from 554 (collided with 554_chat_session_link_read_audit); IF NOT
-- EXISTS keeps it a no-op where 554_project_memory_status already ran.
CREATE TABLE IF NOT EXISTS project_memory_status (
    project_id UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    location_key TEXT NOT NULL,
    path TEXT NOT NULL,
    exists_on_disk BOOLEAN NOT NULL DEFAULT FALSE,
    is_directory BOOLEAN NOT NULL DEFAULT FALSE,
    modified_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    error TEXT,
    PRIMARY KEY (project_id, location_key)
);

CREATE INDEX IF NOT EXISTS idx_project_memory_status_workspace
    ON project_memory_status (workspace_id, project_id);
