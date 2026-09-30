-- Single-use links that open the GitHub App manifest page in a browser.
-- The desktop app and the CLI hand this link to the system browser; the row
-- keeps the signed manifest state server-side so the URL carries only a
-- random token. token_hash is sha256 of that token.
CREATE TABLE github_app_launch_token (
    token_hash TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX github_app_launch_token_expires_idx ON github_app_launch_token (expires_at);
