-- DENE-1719: issues a chat follows besides the ones it opened. The chat's
-- ticket list is origin_chat_session_id (opened here, never moves) plus these
-- rows. source 'auto' is written when the chat's run changes an issue's
-- status or assignee, comments on it or hands it off; 'manual' when someone
-- pins it by hand. hidden is a hand-made take-down: it keeps auto-follow from
-- bringing the issue back and also hides an issue the chat opened.
--
-- No foreign keys (repository rule): readers join issue and chat_session and
-- skip rows whose side is gone. A new table; the previous release never reads it.
CREATE TABLE IF NOT EXISTS chat_followed_issue (
    chat_session_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('auto', 'manual')),
    hidden BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_session_id, issue_id)
);
