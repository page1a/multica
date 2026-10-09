-- DENE-1665: the chat an issue was opened from. One column covers every way a
-- chat opens an issue (an agent's `issue create`, `plan apply`, the manual
-- turn-into-goal), which origin_type/origin_id cannot: a plan stamps its own
-- node id there.
--
-- No foreign key (repository rule). A deleted chat leaves the pointer behind;
-- every reader joins chat_session and treats a missing row as no source.
--
-- A nullable column with no default is a catalog-only change. Bound lock
-- acquisition so the ALTER fails fast instead of queueing an ACCESS EXCLUSIVE
-- lock in front of every issue query. The index follows in 647.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

ALTER TABLE issue ADD COLUMN IF NOT EXISTS origin_chat_session_id UUID;
