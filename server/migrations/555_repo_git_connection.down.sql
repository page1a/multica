UPDATE github_pull_request SET source = 'daemon' WHERE source = 'token';
ALTER TABLE github_pull_request DROP CONSTRAINT IF EXISTS github_pull_request_source_check;
ALTER TABLE github_pull_request
    ADD CONSTRAINT github_pull_request_source_check
    CHECK (source IN ('github_app', 'daemon'));

ALTER TABLE vcs_pull_request DROP COLUMN IF EXISTS checks_rollup_state;
ALTER TABLE vcs_pull_request DROP COLUMN IF EXISTS mergeable_state;

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_workspace_binding;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS last_webhook_at;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS last_lookup_error;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS last_lookup_ok;
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS last_lookup_at;

-- Repo-scoped rows (and GitHub token rows) cannot satisfy the restored
-- instance-wide unique key and provider check. These tables have no foreign
-- keys, so the children go first.
DELETE FROM issue_vcs_pull_request
WHERE pull_request_id IN (
    SELECT id FROM vcs_pull_request
    WHERE connection_id IN (
        SELECT id FROM vcs_connection WHERE repo_url <> '' OR provider = 'github'
    )
);
DELETE FROM vcs_commit_status
WHERE connection_id IN (
    SELECT id FROM vcs_connection WHERE repo_url <> '' OR provider = 'github'
);
DELETE FROM vcs_pull_request
WHERE connection_id IN (
    SELECT id FROM vcs_connection WHERE repo_url <> '' OR provider = 'github'
);
DELETE FROM vcs_connection WHERE repo_url <> '' OR provider = 'github';
ALTER TABLE vcs_connection DROP COLUMN IF EXISTS repo_url;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_workspace_id_instance_url_key
    UNIQUE (workspace_id, instance_url);

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_provider_check;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_provider_check
    CHECK (provider IN ('forgejo', 'gitea', 'gitlab'));
