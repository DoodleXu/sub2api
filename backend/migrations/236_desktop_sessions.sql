-- Keep the stable device identity separate from each refresh-token session.
-- desktop_devices remains the compatibility/DPoP lookup table; this table is
-- the durable session ledger used for audit, device history and future
-- multi-session policy changes. Raw refresh tokens are never stored here.
CREATE TABLE IF NOT EXISTS desktop_sessions (
    session_id VARCHAR(128) PRIMARY KEY,
    device_id VARCHAR(64) NOT NULL REFERENCES desktop_devices(device_id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id VARCHAR(128) NOT NULL,
    refresh_family_id VARCHAR(128) NOT NULL,
    scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
    audience VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idle_expires_at TIMESTAMPTZ,
    absolute_expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_desktop_sessions_user_created
    ON desktop_sessions (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_desktop_sessions_device_active
    ON desktop_sessions (device_id, revoked_at)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_desktop_sessions_family
    ON desktop_sessions (refresh_family_id);

COMMENT ON TABLE desktop_sessions IS
    'Durable desktop refresh-session ledger; raw refresh tokens are kept only in Redis by hash.';

-- Existing service writes intentionally continue targeting desktop_devices so
-- mixed-version application instances remain compatible. This trigger mirrors
-- those writes into the normalized session ledger and marks the previous
-- family revoked when a public key is re-enrolled with a new session id.
CREATE OR REPLACE FUNCTION public.sync_desktop_session_from_device()
RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = public
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.session_id IS DISTINCT FROM NEW.session_id THEN
        UPDATE desktop_sessions
        SET revoked_at = COALESCE(revoked_at, NOW()),
            last_seen_at = NOW()
        WHERE session_id = OLD.session_id;
    END IF;

    INSERT INTO desktop_sessions (
        session_id,
        device_id,
        user_id,
        client_id,
        refresh_family_id,
        scopes,
        audience,
        created_at,
        last_seen_at,
        idle_expires_at,
        absolute_expires_at,
        revoked_at
    ) VALUES (
        NEW.session_id,
        NEW.device_id,
        NEW.user_id,
        NEW.client_id,
        NEW.session_id,
        COALESCE(NEW.scopes, '[]'::jsonb),
        NEW.audience,
        NEW.created_at,
        NEW.last_seen_at,
        NEW.last_seen_at + INTERVAL '30 days',
        NEW.created_at + INTERVAL '90 days',
        NEW.revoked_at
    )
    ON CONFLICT (session_id) DO UPDATE SET
        device_id = EXCLUDED.device_id,
        user_id = EXCLUDED.user_id,
        client_id = EXCLUDED.client_id,
        refresh_family_id = EXCLUDED.refresh_family_id,
        scopes = EXCLUDED.scopes,
        audience = EXCLUDED.audience,
        last_seen_at = EXCLUDED.last_seen_at,
        idle_expires_at = EXCLUDED.idle_expires_at,
        absolute_expires_at = EXCLUDED.absolute_expires_at,
        revoked_at = EXCLUDED.revoked_at;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS desktop_devices_sync_session ON desktop_devices;
CREATE TRIGGER desktop_devices_sync_session
AFTER INSERT OR UPDATE OF device_id, user_id, client_id, scopes, audience,
    session_id, created_at, last_seen_at, revoked_at
ON desktop_devices
FOR EACH ROW
EXECUTE FUNCTION public.sync_desktop_session_from_device();

-- Backfill installations that already have desktop_devices rows before the
-- trigger was introduced. The statement is idempotent and preserves a prior
-- revocation timestamp if one was recorded in the session ledger.
INSERT INTO desktop_sessions (
    session_id,
    device_id,
    user_id,
    client_id,
    refresh_family_id,
    scopes,
    audience,
    created_at,
    last_seen_at,
    idle_expires_at,
    absolute_expires_at,
    revoked_at
)
SELECT
    session_id,
    device_id,
    user_id,
    client_id,
    session_id,
    COALESCE(scopes, '[]'::jsonb),
    audience,
    created_at,
    last_seen_at,
    last_seen_at + INTERVAL '30 days',
    created_at + INTERVAL '90 days',
    revoked_at
FROM desktop_devices
ON CONFLICT (session_id) DO UPDATE SET
    device_id = EXCLUDED.device_id,
    user_id = EXCLUDED.user_id,
    client_id = EXCLUDED.client_id,
    refresh_family_id = EXCLUDED.refresh_family_id,
    scopes = EXCLUDED.scopes,
    audience = EXCLUDED.audience,
    last_seen_at = EXCLUDED.last_seen_at,
    idle_expires_at = EXCLUDED.idle_expires_at,
    absolute_expires_at = EXCLUDED.absolute_expires_at,
    revoked_at = EXCLUDED.revoked_at;
