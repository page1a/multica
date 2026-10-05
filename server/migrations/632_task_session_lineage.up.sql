-- DENE-1345: how each run's CLI session relates to the one before it, so the
-- issue timeline, chat and CLI can say "新会话" or "接着第 N 轮的会话" and
-- name why a session broke. Written at claim, corrected by the daemon's
-- terminal report when a resume falls back to a fresh session. NULL on rows
-- older than this migration: the UI shows nothing for them.
ALTER TABLE agent_task_queue
    ADD COLUMN session_mode TEXT,
    ADD COLUMN resumed_from_task_id UUID,
    ADD COLUMN session_break_reason TEXT;
