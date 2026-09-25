-- A chat with no project shows a "bind this chat" reminder. Dismissing it
-- ("this is a casual chat") is a property of the chat itself, so it follows
-- the creator to another browser. NULL means the reminder is still due.
-- Does not participate in activity ordering — see DismissChatSessionProjectNudge.
ALTER TABLE chat_session
    ADD COLUMN IF NOT EXISTS project_nudge_dismissed_at TIMESTAMPTZ;
