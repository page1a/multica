-- Safe rollback: never widen access. The three-value check cannot hold a
-- guest row, and the only tier a guest could be rewritten to is 'member',
-- which would hand a read-only user write access. So guests lose their
-- membership instead, along with the project_member rows that were their only
-- source of visibility. Re-invite them after rolling forward again.
DELETE FROM project_member pm
USING member m
WHERE m.role = 'guest'
  AND pm.workspace_id = m.workspace_id
  AND pm.member_id = m.user_id;

DELETE FROM member
WHERE role = 'guest';

ALTER TABLE member
    DROP CONSTRAINT IF EXISTS member_role_check;

ALTER TABLE member
    ADD CONSTRAINT member_role_check
        CHECK (role IN ('owner', 'admin', 'member'));
