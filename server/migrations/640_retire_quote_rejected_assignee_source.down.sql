-- Re-allows the value. Rows are not stamped back: which ones were rejected
-- quotes is not recorded once they read as 'agent'.
ALTER TABLE issue
    DROP CONSTRAINT issue_assignee_source_check;

ALTER TABLE issue
    ADD CONSTRAINT issue_assignee_source_check
    CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'quote_rejected', 'router'));
