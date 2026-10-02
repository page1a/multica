DROP TABLE IF EXISTS issue_progress;
DROP TABLE IF EXISTS chat_session_progress;

ALTER TABLE issue
    DROP COLUMN IF EXISTS progress_updated_at,
    DROP COLUMN IF EXISTS progress_author_id,
    DROP COLUMN IF EXISTS progress_author_type,
    DROP COLUMN IF EXISTS progress_tone,
    DROP COLUMN IF EXISTS progress_source,
    DROP COLUMN IF EXISTS progress_text;

ALTER TABLE chat_session
    DROP COLUMN IF EXISTS progress_updated_at,
    DROP COLUMN IF EXISTS progress_author_id,
    DROP COLUMN IF EXISTS progress_author_type,
    DROP COLUMN IF EXISTS progress_tone,
    DROP COLUMN IF EXISTS progress_source,
    DROP COLUMN IF EXISTS progress_text,
    DROP COLUMN IF EXISTS title_locked;
