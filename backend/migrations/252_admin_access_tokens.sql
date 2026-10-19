-- Per-device admin access tokens (sat_...): named, scoped, expiring and individually revocable credentials
-- that act as the admin who created them. Only the SHA-256 of a token is stored.

CREATE TABLE IF NOT EXISTS admin_access_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(64) NOT NULL,
    token_hash CHAR(64) NOT NULL,
    prefix VARCHAR(16) NOT NULL,
    last4 VARCHAR(4) NOT NULL,
    scope VARCHAR(16) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    last_used_ip VARCHAR(64),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_admin_access_tokens_token_hash ON admin_access_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_admin_access_tokens_user ON admin_access_tokens (user_id, created_at DESC);
