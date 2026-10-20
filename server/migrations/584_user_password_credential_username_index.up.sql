-- Case-insensitive username uniqueness. Login looks up lower(username).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS user_password_credential_username_key
    ON user_password_credential (lower(username));
