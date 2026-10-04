DROP TABLE IF EXISTS agent_spawn_record;

ALTER TABLE chat_message DROP COLUMN IF EXISTS linked_session_id;

DROP INDEX IF EXISTS chat_session_origin_client_key_idx;
DROP INDEX IF EXISTS chat_session_origin_session_idx;

ALTER TABLE chat_session
    DROP COLUMN IF EXISTS origin_client_key,
    DROP COLUMN IF EXISTS origin_task_id,
    DROP COLUMN IF EXISTS origin_session_id,
    DROP COLUMN IF EXISTS origin_type;
