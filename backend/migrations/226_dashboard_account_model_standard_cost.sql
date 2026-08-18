-- Preserve account-level standard billing by model so the dashboard can apply
-- each account's current CNY-per-USD cost without rescanning usage_logs.

CREATE TABLE IF NOT EXISTS usage_dashboard_account_model_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    standard_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, account_id, model)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_model_hourly_bucket
    ON usage_dashboard_account_model_hourly (bucket_start DESC);

CREATE TABLE IF NOT EXISTS usage_dashboard_account_model_daily (
    bucket_date DATE NOT NULL,
    account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    standard_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, account_id, model)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_model_daily_bucket
    ON usage_dashboard_account_model_daily (bucket_date DESC);

COMMENT ON TABLE usage_dashboard_account_model_hourly IS
    'Standard billing by dashboard hour, account, and requested model for dynamic real-CNY cost reporting.';

COMMENT ON TABLE usage_dashboard_account_model_daily IS
    'Daily rollup of account/model standard billing for dynamic real-CNY cost reporting.';

-- Existing model aggregates do not contain the account dimension. Rebuild the
-- shared model coverage so both aggregate families become complete together.
UPDATE usage_dashboard_aggregation_watermark
SET model_hourly_aggregated_from = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    model_hourly_last_aggregated_at = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    updated_at = NOW()
WHERE id = 1;
