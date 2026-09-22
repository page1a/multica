-- Safe rollback: never widen access. Rewriting a pending guest invitation to
-- 'member' would turn a read-only invite into a writing one the moment it is
-- accepted, so guest invitations are withdrawn instead. Same rule as 502.
DELETE FROM workspace_invitation
WHERE role = 'guest';

ALTER TABLE workspace_invitation
    DROP CONSTRAINT IF EXISTS workspace_invitation_role_check;

ALTER TABLE workspace_invitation
    ADD CONSTRAINT workspace_invitation_role_check
        CHECK (role IN ('admin', 'member'));
