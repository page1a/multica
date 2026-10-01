ALTER TABLE issue_to_label DROP COLUMN IF EXISTS attached_by_type;
ALTER TABLE issue
    DROP COLUMN IF EXISTS assignee_quote,
    DROP COLUMN IF EXISTS assignee_source_user_id,
    DROP COLUMN IF EXISTS assignee_source;
