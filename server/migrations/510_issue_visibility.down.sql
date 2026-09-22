-- Safe rollback: narrow first, drop second. See 503's down for why dropping
-- the column is the rollback target rather than a widening this migration owns.
UPDATE issue SET visibility = 'private' WHERE visibility <> 'private';

ALTER TABLE issue
    DROP CONSTRAINT IF EXISTS issue_project_visibility_pairing;

ALTER TABLE issue
    DROP CONSTRAINT IF EXISTS issue_visibility_check;

ALTER TABLE issue
    DROP COLUMN IF EXISTS visibility;
