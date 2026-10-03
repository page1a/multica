-- Older schemas cannot represent a rejected quote. Preserve the historical
-- "agent attempt ignored" meaning before restoring the old constraint.
UPDATE issue
SET assignee_source = 'agent'
WHERE assignee_source = 'quote_rejected';

ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'router'));
