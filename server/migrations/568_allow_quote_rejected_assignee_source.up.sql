-- DENE-1138: preserve a rejected per-quote attempt without letting routing
-- replace it with a different executor. Existing rows are unaffected.
ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'quote_rejected', 'router'));
