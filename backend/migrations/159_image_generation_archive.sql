-- Archive generated image outputs for admin review and web-console async image tasks.

CREATE TABLE IF NOT EXISTS image_generation_records (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT REFERENCES users(id) ON DELETE SET NULL,
    api_key_id          BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    group_id            BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    account_id          BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    request_id          VARCHAR(128) NOT NULL DEFAULT '',
    source              VARCHAR(32) NOT NULL DEFAULT 'gateway',
    endpoint            VARCHAR(128) NOT NULL DEFAULT '',
    model               VARCHAR(128) NOT NULL DEFAULT '',
    prompt_excerpt      TEXT NOT NULL DEFAULT '',
    image_count         INTEGER NOT NULL DEFAULT 0,
    status              VARCHAR(32) NOT NULL DEFAULT 'pending',
    storage_type        VARCHAR(32) NOT NULL DEFAULT 'local',
    error_message       TEXT NOT NULL DEFAULT '',
    usage_log_id        BIGINT REFERENCES usage_logs(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    CONSTRAINT chk_image_generation_records_status
        CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped', 'expired')),
    CONSTRAINT chk_image_generation_records_storage_type
        CHECK (storage_type IN ('local', 's3'))
);

CREATE INDEX IF NOT EXISTS idx_image_generation_records_created_at
    ON image_generation_records(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_generation_records_user_created
    ON image_generation_records(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_generation_records_api_key_created
    ON image_generation_records(api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_generation_records_model_created
    ON image_generation_records(model, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_image_generation_records_status_created
    ON image_generation_records(status, created_at DESC);

CREATE TABLE IF NOT EXISTS image_generation_assets (
    id                  BIGSERIAL PRIMARY KEY,
    record_id           BIGINT NOT NULL REFERENCES image_generation_records(id) ON DELETE CASCADE,
    asset_index         INTEGER NOT NULL DEFAULT 0,
    mime_type           VARCHAR(64) NOT NULL DEFAULT '',
    extension           VARCHAR(16) NOT NULL DEFAULT '',
    width               INTEGER,
    height              INTEGER,
    bytes               BIGINT NOT NULL DEFAULT 0,
    sha256              VARCHAR(64) NOT NULL DEFAULT '',
    storage_key         TEXT NOT NULL,
    public_url          TEXT NOT NULL DEFAULT '',
    admin_url           TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_image_generation_assets_record_index UNIQUE (record_id, asset_index)
);

CREATE INDEX IF NOT EXISTS idx_image_generation_assets_record_id
    ON image_generation_assets(record_id, asset_index);
CREATE INDEX IF NOT EXISTS idx_image_generation_assets_sha256
    ON image_generation_assets(sha256);

CREATE TABLE IF NOT EXISTS web_console_image_tasks (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id          BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    session_id          VARCHAR(128) NOT NULL DEFAULT '',
    message_id          VARCHAR(128) NOT NULL DEFAULT '',
    status              VARCHAR(32) NOT NULL DEFAULT 'pending',
    request_json        JSONB NOT NULL DEFAULT '{}'::jsonb,
    record_id           BIGINT REFERENCES image_generation_records(id) ON DELETE SET NULL,
    error_message       TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_web_console_image_tasks_status
        CHECK (status IN ('pending', 'running', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_web_console_image_tasks_user_created
    ON web_console_image_tasks(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_web_console_image_tasks_status_created
    ON web_console_image_tasks(status, created_at ASC);
