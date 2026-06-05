-- Per-user risk-control handling policies and audit snapshots.

CREATE TABLE IF NOT EXISTS content_moderation_user_policies (
    id                     BIGSERIAL PRIMARY KEY,
    user_id                BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    action                 VARCHAR(32) NOT NULL DEFAULT 'block_only',
    block_status           INT NOT NULL DEFAULT 0,
    error_code             VARCHAR(80) NOT NULL DEFAULT 'content_policy_violation',
    block_message          TEXT NOT NULL DEFAULT '',
    ban_threshold          INT NOT NULL DEFAULT 1,
    violation_window_hours INT NOT NULL DEFAULT 720,
    apply_to_hash_block    BOOLEAN NOT NULL DEFAULT FALSE,
    note                   TEXT NOT NULL DEFAULT '',
    created_by             BIGINT REFERENCES users(id) ON DELETE SET NULL,
    updated_by             BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_content_moderation_user_policies_user UNIQUE (user_id),
    CONSTRAINT chk_content_moderation_user_policy_action
        CHECK (action IN ('block_only', 'block_notify', 'block_notify_disable')),
    CONSTRAINT chk_content_moderation_user_policy_block_status
        CHECK (block_status = 0 OR (block_status BETWEEN 400 AND 599)),
    CONSTRAINT chk_content_moderation_user_policy_threshold
        CHECK (ban_threshold > 0),
    CONSTRAINT chk_content_moderation_user_policy_window
        CHECK (violation_window_hours > 0)
);

CREATE INDEX IF NOT EXISTS idx_content_moderation_user_policies_enabled
    ON content_moderation_user_policies(enabled);

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS policy_id BIGINT REFERENCES content_moderation_user_policies(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS policy_action VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS block_status INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS error_code VARCHAR(80) NOT NULL DEFAULT 'content_policy_violation';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_policy_created_at
    ON content_moderation_logs(policy_id, created_at DESC);
