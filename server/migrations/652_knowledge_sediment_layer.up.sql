-- Boss-layer sediment (DENE-1680). layer is 'worker' for an executor's close
-- and 'boss' for the round a parent ticket or project opened, or a chat that
-- settled its work. sources names what a boss-layer round sums up:
-- [{kind: issue|project, id}]. Additive columns with defaults; the previous
-- release neither reads nor writes them.
ALTER TABLE knowledge_sediment
    ADD COLUMN layer TEXT NOT NULL DEFAULT 'worker',
    ADD COLUMN sources JSONB NOT NULL DEFAULT '[]'::jsonb;
