-- Account-level cost aggregates for dashboard CNY/USD cost ratios.
-- These tables are intentionally populated by the existing dashboard aggregation
-- background job, not by this migration, so startup never scans usage_logs.

CREATE TABLE IF NOT EXISTS usage_dashboard_account_cost_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    account_id BIGINT NOT NULL,
    platform VARCHAR(50),
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, account_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_hourly_bucket
    ON usage_dashboard_account_cost_hourly (bucket_start DESC);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_hourly_account
    ON usage_dashboard_account_cost_hourly (account_id, bucket_start DESC);

COMMENT ON TABLE usage_dashboard_account_cost_hourly IS 'Pre-aggregated account-level hourly cost for dashboard CNY/USD ratios.';
COMMENT ON COLUMN usage_dashboard_account_cost_hourly.account_cost IS 'SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)) for this account and hour.';

CREATE TABLE IF NOT EXISTS usage_dashboard_account_cost_daily (
    bucket_date DATE NOT NULL,
    account_id BIGINT NOT NULL,
    platform VARCHAR(50),
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, account_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_daily_bucket
    ON usage_dashboard_account_cost_daily (bucket_date DESC);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_daily_account
    ON usage_dashboard_account_cost_daily (account_id, bucket_date DESC);

COMMENT ON TABLE usage_dashboard_account_cost_daily IS 'Pre-aggregated account-level daily cost for dashboard CNY/USD ratios.';
COMMENT ON COLUMN usage_dashboard_account_cost_daily.account_cost IS 'Rolled-up account cost for this account and local dashboard day.';
