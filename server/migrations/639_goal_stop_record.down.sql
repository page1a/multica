ALTER TABLE issue_goal
    DROP COLUMN IF EXISTS stop_reason,
    DROP COLUMN IF EXISTS stopped_on_behalf_of,
    DROP COLUMN IF EXISTS stopped_by_id,
    DROP COLUMN IF EXISTS stopped_by_type;
