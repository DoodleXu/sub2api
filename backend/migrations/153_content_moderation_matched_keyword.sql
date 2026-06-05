-- Store the exact keyword that triggered a content moderation keyword block.

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS matched_keyword VARCHAR(200) NOT NULL DEFAULT '';

COMMENT ON COLUMN content_moderation_logs.matched_keyword IS 'Configured keyword that matched and caused a keyword_block action.';
