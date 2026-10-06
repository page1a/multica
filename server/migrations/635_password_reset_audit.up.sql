-- One row per password reset (DENE-1416): whose password changed, how, and
-- who did it. 'self' is the forgot-password flow gated by the team 2FA code;
-- 'admin' is a workspace owner issuing a temporary password.
--
-- No foreign keys and no cascades, per repository policy: an audit row must
-- outlive the user and workspace it describes.
CREATE TABLE IF NOT EXISTS password_reset_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    method TEXT NOT NULL CHECK (method IN ('self', 'admin')),
    actor_user_id UUID,
    workspace_id UUID,
    client_ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_password_reset_audit_user
    ON password_reset_audit (user_id, created_at DESC);
