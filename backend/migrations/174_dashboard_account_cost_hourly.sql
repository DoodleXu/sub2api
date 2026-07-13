-- Keep the dashboard CNY cost cards off the usage_logs hot path.
-- The background aggregation job populates this table; the dashboard query only
-- scans raw rows outside the contiguous aggregate coverage window.

CREATE TABLE IF NOT EXISTS usage_dashboard_account_cost_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    account_id BIGINT NOT NULL,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, account_id)
);

COMMENT ON TABLE usage_dashboard_account_cost_hourly IS 'Pre-aggregated hourly account cost used by dashboard CNY cost cards.';
COMMENT ON COLUMN usage_dashboard_account_cost_hourly.account_cost IS 'SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)) for this account and dashboard hour.';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS account_cost_hourly_aggregated_from TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS account_cost_hourly_last_aggregated_at TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00';

COMMENT ON COLUMN usage_dashboard_aggregation_watermark.account_cost_hourly_aggregated_from IS 'Earliest time in the contiguous range covered by account-cost hourly aggregates.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at IS 'Exclusive end time of the contiguous range covered by account-cost hourly aggregates.';
