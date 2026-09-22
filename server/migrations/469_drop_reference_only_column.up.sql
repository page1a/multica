-- Drop reference_only, the contract half of the pair MUL-7072 started. Since that
-- release a bare body mention of an issue key writes no link row at all, and no
-- query reads or writes this column. It may only run once every instance of the
-- previous release is gone: an older instance still names the column in its link
-- INSERT and would fail on every link write against this schema.
--
-- The DELETE repeats as a mop-up. Migration 462 cleared the historical hidden
-- rows, but instances still serving during that rollout could write a few more,
-- and once the column is gone such a row becomes indistinguishable from a real
-- link — visible in the issue's PR list and, while its PR is in flight, blocking
-- the issue from auto-advancing.
--
-- Idempotent on purpose. This migration is the same change as 468 (upstream
-- renumbered its copy from 469 to 468 before release), and the ledger keys on
-- the complete filename, so both files run in filename order on the same
-- database: 468 drops the column and this one then finds it gone. The DELETE is
-- guarded as well, because it names the column the drop removes — every
-- statement here has to tolerate the object that 468 already took away.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'issue_pull_request' AND column_name = 'reference_only'
    ) THEN
        DELETE FROM issue_pull_request WHERE reference_only;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'issue_vcs_pull_request' AND column_name = 'reference_only'
    ) THEN
        DELETE FROM issue_vcs_pull_request WHERE reference_only;
    END IF;
END
$$;

ALTER TABLE issue_pull_request DROP COLUMN IF EXISTS reference_only;
ALTER TABLE issue_vcs_pull_request DROP COLUMN IF EXISTS reference_only;
