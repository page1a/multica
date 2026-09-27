UPDATE chat_session SET visibility = 'project' WHERE visibility = 'workspace';
ALTER TABLE chat_session DROP CONSTRAINT IF EXISTS chat_session_visibility_check;
ALTER TABLE chat_session
    ADD CONSTRAINT chat_session_visibility_check
    CHECK (visibility IN ('private', 'project'));
