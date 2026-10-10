DROP TABLE IF EXISTS review_pass_head;

ALTER TABLE github_pull_request
    DROP COLUMN IF EXISTS approved_head_sha,
    DROP COLUMN IF EXISTS approved_at,
    DROP COLUMN IF EXISTS approved_by;
