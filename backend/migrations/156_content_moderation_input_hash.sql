-- 内容审计记录输入 hash，便于人工复核后复制或加白

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS input_hash VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_content_moderation_logs_input_hash
    ON content_moderation_logs(input_hash)
    WHERE input_hash <> '';
