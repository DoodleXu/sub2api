-- Keep authenticated user history scans bounded as image_task_history grows.
CREATE INDEX IF NOT EXISTS idx_image_task_history_user_created
    ON image_task_history (user_id, created_at DESC, task_id DESC);
