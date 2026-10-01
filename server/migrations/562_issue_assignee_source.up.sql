-- DENE-1033: record whose decision an executor assignment is.
--
-- assignee_source: 'human' (a member picked it), 'automation' (an autopilot
-- the member configured), 'quote' (an agent assigned it on the word of the
-- person who started its run, and the server checked that word), 'agent' (an
-- agent chose it on its own — routing ignores it), 'router' (the routing
-- module filled the slot). NULL is every ticket that predates this column and
-- every write path that does not stamp a source; it keeps meaning "leave the
-- assignee alone".
--
-- With source 'agent' and an empty assignee the row records an ignored
-- attempt: an agent named somebody, the server dropped the name, and routing
-- says so in its comment.
ALTER TABLE issue
    ADD COLUMN assignee_source TEXT
        CHECK (assignee_source IN ('human', 'automation', 'quote', 'agent', 'router')),
    ADD COLUMN assignee_source_user_id UUID,
    ADD COLUMN assignee_quote TEXT;

-- Who put a label on an issue. Routing reads a tier label as an instruction,
-- so a label an agent stuck on must not be read that way. NULL = predates the
-- column, treated as a person's.
ALTER TABLE issue_to_label
    ADD COLUMN attached_by_type TEXT;
