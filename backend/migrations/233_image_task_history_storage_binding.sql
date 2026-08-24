ALTER TABLE image_task_history
    ADD COLUMN IF NOT EXISTS storage_binding_id VARCHAR(96) NOT NULL DEFAULT '';

COMMENT ON COLUMN image_task_history.storage_binding_id IS
    'Non-secret fingerprint of the object-storage binding used by the task; prevents cross-bucket cleanup and URL signing after configuration changes.';
