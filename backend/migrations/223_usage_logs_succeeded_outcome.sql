-- Keep historical rows nullable so their established actual_cost fallback is
-- preserved. New raw usage-log inserts omit this column and receive TRUE from
-- the database default; non-success paths must explicitly write FALSE.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS succeeded BOOLEAN;
ALTER TABLE usage_logs ALTER COLUMN succeeded SET DEFAULT TRUE;

CREATE INDEX IF NOT EXISTS idx_usage_logs_succeeded_created_at
    ON usage_logs (created_at DESC) WHERE succeeded IS TRUE;
