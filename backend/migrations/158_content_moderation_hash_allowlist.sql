-- Persist risk-control input hash allowlist and operation audit trail.

CREATE TABLE IF NOT EXISTS content_moderation_allowed_hashes (
    input_hash          VARCHAR(64) PRIMARY KEY,
    source              VARCHAR(32) NOT NULL DEFAULT 'manual',
    source_log_id       BIGINT REFERENCES content_moderation_logs(id) ON DELETE SET NULL,
    note                TEXT NOT NULL DEFAULT '',
    created_by          BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_content_moderation_allowed_hash_source
        CHECK (source IN ('manual', 'audit_log', 'legacy_redis'))
);

CREATE INDEX IF NOT EXISTS idx_content_moderation_allowed_hashes_created_at
    ON content_moderation_allowed_hashes(created_at DESC);

CREATE TABLE IF NOT EXISTS content_moderation_allowed_hash_events (
    id                  BIGSERIAL PRIMARY KEY,
    action              VARCHAR(16) NOT NULL,
    input_hash          VARCHAR(64) NOT NULL DEFAULT '',
    actor_id            BIGINT REFERENCES users(id) ON DELETE SET NULL,
    source_log_id       BIGINT REFERENCES content_moderation_logs(id) ON DELETE SET NULL,
    note                TEXT NOT NULL DEFAULT '',
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_content_moderation_allowed_hash_event_action
        CHECK (action IN ('add', 'delete', 'clear'))
);

CREATE INDEX IF NOT EXISTS idx_content_moderation_allowed_hash_events_created_at
    ON content_moderation_allowed_hash_events(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_content_moderation_allowed_hash_events_input_hash
    ON content_moderation_allowed_hash_events(input_hash, created_at DESC)
    WHERE input_hash <> '';
