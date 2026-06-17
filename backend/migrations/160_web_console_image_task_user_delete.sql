ALTER TABLE web_console_image_tasks
    ADD COLUMN IF NOT EXISTS user_deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_web_console_image_tasks_user_session_active
    ON web_console_image_tasks(user_id, session_id, created_at DESC)
    WHERE user_deleted_at IS NULL;
