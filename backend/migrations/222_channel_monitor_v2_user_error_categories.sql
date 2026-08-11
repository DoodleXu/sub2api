-- Error categories must carry the same user dimension as user metrics. The
-- prior global-only rows made user rankings apply a window-wide approximation.
ALTER TABLE channel_monitor_v2_error_metrics_1m
    ADD COLUMN IF NOT EXISTS user_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_monitor_v2_error_metrics_1m
    DROP CONSTRAINT IF EXISTS channel_monitor_v2_error_metrics_1m_pkey;
ALTER TABLE channel_monitor_v2_error_metrics_1m
    ADD PRIMARY KEY (bucket_start, platform, group_id, model, user_id, error_category, taxonomy_version);
CREATE INDEX IF NOT EXISTS idx_channel_monitor_v2_errors_user_time
    ON channel_monitor_v2_error_metrics_1m (user_id, bucket_start DESC);

ALTER TABLE channel_monitor_v2_error_metrics_rollup
    ADD COLUMN IF NOT EXISTS user_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_monitor_v2_error_metrics_rollup
    DROP CONSTRAINT IF EXISTS channel_monitor_v2_error_metrics_rollup_pkey;
ALTER TABLE channel_monitor_v2_error_metrics_rollup
    ADD PRIMARY KEY (bucket_seconds, bucket_start, platform, group_id, model, user_id, error_category, taxonomy_version);
CREATE INDEX IF NOT EXISTS idx_channel_monitor_v2_errors_rollup_user_time
    ON channel_monitor_v2_error_metrics_rollup (bucket_seconds, user_id, bucket_start DESC);

-- Historical category rows have no user attribution. Rebuild the bounded
-- retention window from ops_error_logs rather than fabricating per-user rates.
DELETE FROM channel_monitor_v2_error_metrics_1m;
DELETE FROM channel_monitor_v2_error_metrics_rollup;
UPDATE channel_monitor_v2_watermarks
SET error_coverage_start = NULL,
    backfill_cursor = LEAST(
        COALESCE(backfill_cursor, date_trunc('minute', NOW())),
        date_trunc('minute', NOW()) - INTERVAL '90 days'
    ),
    updated_at = NOW()
WHERE id = 1;
