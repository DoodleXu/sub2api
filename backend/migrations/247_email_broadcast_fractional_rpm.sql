-- Allow email broadcast throttling below one message per minute.
ALTER TABLE notification_email_broadcast_jobs
    ALTER COLUMN rpm TYPE DECIMAL(6,2) USING rpm::DECIMAL(6,2);

ALTER TABLE notification_email_broadcast_jobs
    DROP CONSTRAINT IF EXISTS chk_notification_email_broadcast_rpm;

ALTER TABLE notification_email_broadcast_jobs
    ADD CONSTRAINT chk_notification_email_broadcast_rpm CHECK (rpm BETWEEN 0.01 AND 30);
