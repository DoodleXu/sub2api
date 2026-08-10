-- Durable state for asynchronous Grok/xAI video tasks. Redis is only an
-- acceleration layer, so ownership, pricing inputs and one-shot billing state
-- must survive cache eviction, restarts and multi-instance routing.
CREATE TABLE IF NOT EXISTS grok_video_tasks (
    request_id VARCHAR(255) NOT NULL,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT,
    account_id BIGINT NOT NULL,
    model VARCHAR(255) NOT NULL,
    billing_model VARCHAR(255),
    upstream_model VARCHAR(255),
    original_model VARCHAR(255),
    video_resolution VARCHAR(10),
    video_duration_seconds INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    billing_claimed_at TIMESTAMPTZ,
    billed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, user_id, api_key_id),
    CONSTRAINT chk_grok_video_tasks_duration_positive
        CHECK (video_duration_seconds IS NULL OR video_duration_seconds > 0)
);

CREATE INDEX IF NOT EXISTS idx_grok_video_tasks_created_at
    ON grok_video_tasks (created_at);
