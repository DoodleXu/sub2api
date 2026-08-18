-- Durable admin email broadcast jobs. Runtime progress must not be stored as a
-- repeatedly rewritten JSON blob in settings: one recipient is one row so
-- updates stay bounded and restart recovery can be fenced by a database lease.
CREATE TABLE IF NOT EXISTS notification_email_broadcast_jobs (
    batch_id VARCHAR(64) PRIMARY KEY,
    status VARCHAR(32) NOT NULL,
    scope VARCHAR(32) NOT NULL,
    locale VARCHAR(16) NOT NULL,
    message_title VARCHAR(200) NOT NULL,
    message_html TEXT NOT NULL,
    action_label VARCHAR(200),
    action_url TEXT,
    rpm INTEGER NOT NULL,
    content_hash VARCHAR(64) NOT NULL,
    created_by_user_id BIGINT,
    created_by_email VARCHAR(320),
    target_count INTEGER NOT NULL DEFAULT 0,
    sent_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    unsubscribed_count INTEGER NOT NULL DEFAULT 0,
    failure_count INTEGER NOT NULL DEFAULT 0,
    uncertain_count INTEGER NOT NULL DEFAULT 0,
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    last_error TEXT,
    lease_owner VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
	CONSTRAINT chk_notification_email_broadcast_job_status CHECK (
		status IN ('running', 'canceling', 'completed', 'canceled', 'interrupted')
	),
    CONSTRAINT chk_notification_email_broadcast_rpm CHECK (rpm BETWEEN 1 AND 30),
    CONSTRAINT chk_notification_email_broadcast_counts CHECK (
        target_count >= 0 AND sent_count >= 0 AND skipped_count >= 0 AND
		unsubscribed_count >= 0 AND failure_count >= 0 AND uncertain_count >= 0 AND
		sent_count + skipped_count + failure_count + uncertain_count <= target_count AND
		unsubscribed_count <= skipped_count
    )
);

CREATE INDEX IF NOT EXISTS idx_notification_email_broadcast_jobs_updated
    ON notification_email_broadcast_jobs (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_email_broadcast_jobs_recovery
    ON notification_email_broadcast_jobs (status, lease_expires_at)
    WHERE status IN ('running', 'canceling');
-- A single shared SMTP transport must not run multiple broadcast campaigns at
-- once. This also fences concurrent requests across application nodes.
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_email_broadcast_one_active
    ON notification_email_broadcast_jobs ((1))
    WHERE status IN ('running', 'canceling');

CREATE TABLE IF NOT EXISTS notification_email_broadcast_recipients (
    batch_id VARCHAR(64) NOT NULL REFERENCES notification_email_broadcast_jobs(batch_id) ON DELETE CASCADE,
    email VARCHAR(320) NOT NULL,
    normalized_email VARCHAR(320) NOT NULL,
    user_id BIGINT,
    recipient_name VARCHAR(255),
    locale VARCHAR(16) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    error_code VARCHAR(64),
    last_error TEXT,
    message_id VARCHAR(255) NOT NULL,
    accepted_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (batch_id, normalized_email),
	CONSTRAINT chk_notification_email_broadcast_attempts CHECK (attempt_count >= 0),
	CONSTRAINT chk_notification_email_broadcast_recipient_status CHECK (
		status IN ('pending', 'sending', 'sent', 'skipped', 'retry', 'failed', 'uncertain')
	)
);

CREATE INDEX IF NOT EXISTS idx_notification_email_broadcast_recipients_status
    ON notification_email_broadcast_recipients (batch_id, status, normalized_email);

CREATE TABLE IF NOT EXISTS notification_email_broadcast_drafts (
    draft_key VARCHAR(64) PRIMARY KEY,
    payload JSONB NOT NULL,
    saved_by_user_id BIGINT,
    saved_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
