CREATE TABLE IF NOT EXISTS image_task_history (
    task_id         VARCHAR(96) PRIMARY KEY,
    user_id         BIGINT NOT NULL,
    api_key_id      BIGINT NOT NULL,
    platform        VARCHAR(32) NOT NULL DEFAULT '',
    operation       VARCHAR(32) NOT NULL DEFAULT '',
    model           VARCHAR(128) NOT NULL DEFAULT '',
    image_count     INTEGER NOT NULL DEFAULT 0,
    status          VARCHAR(32) NOT NULL,
    http_status     INTEGER NOT NULL DEFAULT 0,
    result_json     JSONB,
    error_json      JSONB,
    created_at      TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ NOT NULL,
    CONSTRAINT chk_image_task_history_status
        CHECK (status IN ('processing', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_image_task_history_created
    ON image_task_history(created_at DESC, task_id DESC);
CREATE INDEX IF NOT EXISTS idx_image_task_history_status_created
    ON image_task_history(status, created_at DESC, task_id DESC);
