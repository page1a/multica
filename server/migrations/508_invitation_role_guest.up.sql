-- Invite someone straight into the guest tier (DENE-697).
-- 502 widened member.role; an invitation still could not carry 'guest', so
-- the only way to produce a guest was to invite a full member and demote
-- them — a window in which they hold write access. This closes it. Widening
-- the allowed set changes no existing row.
ALTER TABLE workspace_invitation
    DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;

ALTER TABLE workspace_invitation
    ADD CONSTRAINT workspace_invitation_role_check
        CHECK (role IN ('admin', 'member', 'guest'));
