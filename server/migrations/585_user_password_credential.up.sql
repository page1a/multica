-- Self-host username/password accounts beyond the single env-var operator.
-- No foreign key: the repo forbids them. Login resolves user_id in
-- application code and treats a dangling row as invalid credentials.
CREATE TABLE IF NOT EXISTS user_password_credential (
    user_id UUID PRIMARY KEY,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
