ALTER TABLE image_task_history
    ADD COLUMN IF NOT EXISTS result_object_keys JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN image_task_history.result_object_keys IS
    'Private durable object keys for regenerating admin history URLs; never expose directly to API clients.';
