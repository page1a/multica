UPDATE issue_property
SET archived_at = NULL, updated_at = now()
WHERE name = '验收席' AND type = 'select';

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_reviewer_type_check;
ALTER TABLE issue DROP COLUMN IF EXISTS reviewer_type;
ALTER TABLE issue DROP COLUMN IF EXISTS reviewer_id;
