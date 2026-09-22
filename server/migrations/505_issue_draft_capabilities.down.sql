-- Reverses 492. The recorded capability keys and version go with the columns
-- that carry them: a build that cannot assemble a capability fragment must not
-- keep claiming it ran one.
ALTER TABLE issue_draft
    DROP COLUMN IF EXISTS capability_version,
    DROP COLUMN IF EXISTS capability_keys;
