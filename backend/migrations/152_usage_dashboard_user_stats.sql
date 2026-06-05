-- User-dimension dashboard aggregates.
-- These tables let admin dashboard user trend and spending ranking read compact
-- hourly/daily rows instead of grouping large usage_logs ranges on every load.

CREATE TABLE IF NOT EXISTS usage_dashboard_hourly_user_stats (
    bucket_start TIMESTAMPTZ NOT NULL,
    user_id BIGINT NOT NULL,
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, user_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_hourly_user_stats_user_bucket
    ON usage_dashboard_hourly_user_stats (user_id, bucket_start DESC);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_hourly_user_stats_bucket
    ON usage_dashboard_hourly_user_stats (bucket_start DESC);

COMMENT ON TABLE usage_dashboard_hourly_user_stats IS 'Pre-aggregated hourly usage metrics by user for admin dashboard.';
COMMENT ON COLUMN usage_dashboard_hourly_user_stats.account_cost IS 'SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)) for this dashboard hour/user.';

CREATE TABLE IF NOT EXISTS usage_dashboard_daily_user_stats (
    bucket_date DATE NOT NULL,
    user_id BIGINT NOT NULL,
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, user_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_daily_user_stats_user_bucket
    ON usage_dashboard_daily_user_stats (user_id, bucket_date DESC);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_daily_user_stats_bucket
    ON usage_dashboard_daily_user_stats (bucket_date DESC);

COMMENT ON TABLE usage_dashboard_daily_user_stats IS 'Pre-aggregated daily usage metrics by user for admin dashboard.';
COMMENT ON COLUMN usage_dashboard_daily_user_stats.account_cost IS 'Rolled-up account-billed cost for this dashboard day/user.';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS user_hourly_aggregated_from TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS user_hourly_last_aggregated_at TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS user_daily_aggregated_from TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS user_daily_last_aggregated_at TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

COMMENT ON COLUMN usage_dashboard_aggregation_watermark.user_hourly_aggregated_from IS 'Earliest time in the contiguous range covered by user-dimension hourly dashboard aggregates.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.user_hourly_last_aggregated_at IS 'Exclusive end time of the contiguous range covered by user-dimension hourly dashboard aggregates.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.user_daily_aggregated_from IS 'Earliest time in the contiguous full-day range covered by user-dimension daily dashboard aggregates.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.user_daily_last_aggregated_at IS 'Exclusive end time of the contiguous full-day range covered by user-dimension daily dashboard aggregates.';
