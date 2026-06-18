ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS email_deduped BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS last_email_sent_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_last_email_sent_at
    ON content_moderation_logs(last_email_sent_at DESC)
    WHERE last_email_sent_at IS NOT NULL;
