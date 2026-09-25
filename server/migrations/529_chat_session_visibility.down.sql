DROP TABLE IF EXISTS chat_visibility_notice;

ALTER TABLE chat_message DROP COLUMN IF EXISTS sender_user_id;

DROP TABLE IF EXISTS chat_session_read;

DELETE FROM resource_share WHERE resource_type = 'chat_session';

ALTER TABLE resource_share DROP CONSTRAINT IF EXISTS resource_share_access_check;
ALTER TABLE resource_share DROP COLUMN IF EXISTS access;

ALTER TABLE resource_share DROP CONSTRAINT IF EXISTS resource_share_resource_type_check;
ALTER TABLE resource_share
    ADD CONSTRAINT resource_share_resource_type_check
    CHECK (resource_type IN ('issue', 'repo'));

ALTER TABLE chat_session DROP CONSTRAINT IF EXISTS chat_session_visibility_check;
ALTER TABLE chat_session DROP COLUMN IF EXISTS visibility;
