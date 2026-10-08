-- DENE-1613: an unverified quote no longer holds a ticket; it is an ordinary
-- agent pick and routing fills the slot. Rows stamped by the old rule read as
-- that, and the value leaves the schema so nothing can write it again. The
-- tickets still waiting in todo/backlog are re-routed once after deploy.
UPDATE issue
SET assignee_source = 'agent'
WHERE assignee_source = 'quote_rejected';

ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'router'));
