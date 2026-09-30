-- One GitHub App identity for this deployment, created from Settings.
-- Environment variables stay ahead of this row; the process loads the row
-- when those variables are empty. Secrets are secretbox ciphertext.
CREATE TABLE github_app_credential (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    app_id BIGINT NOT NULL,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    html_url TEXT NOT NULL,
    manage_url TEXT NOT NULL,
    client_id TEXT NOT NULL DEFAULT '',
    private_key BYTEA NOT NULL,
    webhook_secret BYTEA NOT NULL,
    client_secret BYTEA NOT NULL,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    workspace_id UUID REFERENCES workspace(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
