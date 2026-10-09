-- dispatch_projects limits automatic dispatch to tickets in these projects
-- (DENE-1648). Empty — the default — means every project. A seat that only
-- suits one project keeps its tier and stays auto-pickable there, while
-- tickets elsewhere never see it as a candidate. @mention, assignment and
-- delegation are not limited, the same as mention_only.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS dispatch_projects UUID[] NOT NULL DEFAULT '{}';
