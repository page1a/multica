-- Record which authority supplied a pull-request snapshot. GitHub App webhooks
-- remain the default; the local daemon can report the same PR without an App.
ALTER TABLE github_pull_request
  ADD COLUMN source TEXT NOT NULL DEFAULT 'github_app'
  CHECK (source IN ('github_app', 'daemon'));
