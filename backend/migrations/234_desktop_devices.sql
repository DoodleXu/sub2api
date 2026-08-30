-- Desktop helper device sessions. Secrets are never persisted here: only the
-- public-key-thumbprint device id, granted scopes and refresh family
-- binding are retained so sessions can be listed and revoked.
CREATE TABLE IF NOT EXISTS desktop_devices (
    device_id VARCHAR(64) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id VARCHAR(128) NOT NULL,
    device_name VARCHAR(128) NOT NULL,
    public_key_thumbprint VARCHAR(128) NOT NULL,
    -- Canonical public JWK used to verify DPoP proofs.  It never contains the
    -- private EC scalar and is safe to retain alongside the thumbprint.
    public_key_jwk JSONB NOT NULL DEFAULT '{}'::jsonb,
    dpop_nonce VARCHAR(128) NOT NULL DEFAULT '',
    protection_level VARCHAR(32) NOT NULL DEFAULT 'software',
    scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    audience VARCHAR(128) NOT NULL,
    session_id VARCHAR(128) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_desktop_devices_user_created
    ON desktop_devices (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_desktop_devices_active
    ON desktop_devices (user_id, revoked_at)
    WHERE revoked_at IS NULL;
