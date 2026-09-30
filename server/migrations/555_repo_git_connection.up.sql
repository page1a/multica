-- Repo-scoped Git connections (DENE-961 / DENE-966).
--
-- vcs_connection was one row per workspace and instance. A GitHub token is
-- chosen per repository, so the binding becomes (workspace, instance, repo).
-- repo_url is empty for the existing instance-wide GitLab/Forgejo rows and a
-- repoident key (host/owner/name) when the token belongs to one repository.
-- GitHub joins the provider list; its pull requests stay in github_pull_request.

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_provider_check;
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_provider_check
    CHECK (provider IN ('forgejo', 'gitea', 'gitlab', 'github'));

ALTER TABLE vcs_connection DROP CONSTRAINT IF EXISTS vcs_connection_workspace_id_instance_url_key;
ALTER TABLE vcs_connection ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE vcs_connection
    ADD CONSTRAINT vcs_connection_workspace_binding
    UNIQUE (workspace_id, instance_url, repo_url);

ALTER TABLE vcs_connection ADD COLUMN last_lookup_at TIMESTAMPTZ;
ALTER TABLE vcs_connection ADD COLUMN last_lookup_ok BOOLEAN;
ALTER TABLE vcs_connection ADD COLUMN last_lookup_error TEXT NOT NULL DEFAULT '';
ALTER TABLE vcs_connection ADD COLUMN last_webhook_at TIMESTAMPTZ;

-- Live token lookups remember whether the MR can merge. Webhooks leave these
-- null until a lookup fills them; the close gate reads them the same way it
-- reads GitHub's mergeable_state / checks rollup.
ALTER TABLE vcs_pull_request ADD COLUMN mergeable_state TEXT;
ALTER TABLE vcs_pull_request ADD COLUMN checks_rollup_state TEXT;

-- A token lookup is a third source. Daemon reports must not be stored as this
-- value, and an older check only allowed github_app and daemon.
ALTER TABLE github_pull_request DROP CONSTRAINT IF EXISTS github_pull_request_source_check;
ALTER TABLE github_pull_request
    ADD CONSTRAINT github_pull_request_source_check
    CHECK (source IN ('github_app', 'daemon', 'token'));
