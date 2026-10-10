-- DENE-1678: who approved a pull request on GitHub, when, and at which head
-- commit. Written by the pull_request_review webhook and by gh reports; read
-- by the review skip that closes an in_review ticket whose PR is already
-- reviewed and merged. An approval counts only for the head it saw.
-- Nullable, so the previous release keeps working against the new schema.
ALTER TABLE github_pull_request
    ADD COLUMN approved_by TEXT,
    ADD COLUMN approved_at TIMESTAMPTZ,
    ADD COLUMN approved_head_sha TEXT;

-- The head of every PR linked to the issue at the moment a `verdict: pass`
-- comment was written: the version that pass reviewed. A pass with no row
-- here, or whose head differs from what merged, never lets a ticket skip
-- acceptance. New table, so the previous release never reads it.
CREATE TABLE review_pass_head (
    comment_id UUID NOT NULL REFERENCES comment(id) ON DELETE CASCADE,
    pr_url     TEXT NOT NULL,
    head_sha   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, pr_url)
);
