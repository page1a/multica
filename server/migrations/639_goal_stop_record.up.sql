-- DENE-1583: a stopped goal records who stopped it, on whose word, and why.
-- The server brake stops as 'system'; a person or an agent stops on purpose.
ALTER TABLE issue_goal
    ADD COLUMN stopped_by_type TEXT CHECK (stopped_by_type IN ('member', 'agent', 'system')),
    ADD COLUMN stopped_by_id UUID,
    ADD COLUMN stopped_on_behalf_of UUID,
    ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
-- Goals stopped before this migration keep a NULL stopper: nobody recorded it.
