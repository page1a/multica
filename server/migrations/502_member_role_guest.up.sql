-- Fourth workspace tier: guest, a read-only member (DENE-696).
-- This only widens the set of values the column can hold. No existing row
-- changes tier, and nothing can assign 'guest' through the API until the
-- read-only enforcement layer lands, so the constraint change alone grants
-- nobody anything. The tier semantics live in internal/permission.
ALTER TABLE member
    DROP CONSTRAINT IF EXISTS member_role_check;

ALTER TABLE member
    ADD CONSTRAINT member_role_check
        CHECK (role IN ('owner', 'admin', 'member', 'guest'));
