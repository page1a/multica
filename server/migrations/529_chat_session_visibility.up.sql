-- Chat joins the resource visibility model (DENE-840).
--
-- A chat bound to a project is visible to that project's members (any of the
-- attached projects) and they can speak. A chat with no project, or one the
-- creator marked private, stays visible only to its creator. People outside
-- the project are named in resource_share with access view or speak.
--
-- Old bound chats are backfilled to 'project' on purpose: that is the
-- product default, and the one-time notice tells each creator how many of
-- their chats just opened up.

ALTER TABLE chat_session
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private';

ALTER TABLE chat_session
    ADD CONSTRAINT chat_session_visibility_check
    CHECK (visibility IN ('private', 'project'));

UPDATE chat_session AS cs
   SET visibility = 'project'
 WHERE cs.project_id IS NOT NULL
    OR EXISTS (
        SELECT 1 FROM chat_session_project AS csp
        WHERE csp.chat_session_id = cs.id
    );

ALTER TABLE resource_share
    DROP CONSTRAINT IF EXISTS resource_share_resource_type_check;

ALTER TABLE resource_share
    ADD CONSTRAINT resource_share_resource_type_check
    CHECK (resource_type IN ('issue', 'repo', 'chat_session'));

-- view: can open the chat. speak: can also send. Issue and repo shares
-- ignore this column; the default keeps their existing meaning (see only).
ALTER TABLE resource_share
    ADD COLUMN access TEXT NOT NULL DEFAULT 'view';

ALTER TABLE resource_share
    ADD CONSTRAINT resource_share_access_check
    CHECK (access IN ('view', 'speak'));

-- One read cursor per person. The session-level last_read_at stays as the
-- creator's mirror so a rolling reader of that column does not go blank.
CREATE TABLE chat_session_read (
    chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    last_read_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chat_session_id, user_id)
);

INSERT INTO chat_session_read (chat_session_id, user_id, last_read_at)
SELECT id, creator_id, last_read_at FROM chat_session
ON CONFLICT DO NOTHING;

ALTER TABLE chat_message
    ADD COLUMN sender_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL;

-- Dismissed once per person per workspace. Absent row = the notice is still due.
CREATE TABLE chat_visibility_notice (
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);
