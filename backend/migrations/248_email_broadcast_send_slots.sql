ALTER TABLE notification_email_broadcast_jobs
    ADD COLUMN IF NOT EXISTS next_send_at TIMESTAMPTZ;
