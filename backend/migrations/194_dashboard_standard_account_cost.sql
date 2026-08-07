-- Keep the standard billed USD amount alongside the account-billed amount.
-- Per-dollar CNY cost must use standard billing and must not include account
-- multipliers, while actual account cost metrics continue using account_cost.

ALTER TABLE usage_dashboard_account_cost_hourly
    ADD COLUMN IF NOT EXISTS standard_account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_account_cost_daily
    ADD COLUMN IF NOT EXISTS standard_account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

COMMENT ON COLUMN usage_dashboard_account_cost_hourly.standard_account_cost IS
    'SUM(total_cost) for this account and hour, without account billing multipliers.';

COMMENT ON COLUMN usage_dashboard_account_cost_daily.standard_account_cost IS
    'Rolled-up SUM(total_cost) for this account and local dashboard day.';

UPDATE usage_dashboard_aggregation_watermark
SET account_cost_hourly_aggregated_from = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    account_cost_hourly_last_aggregated_at = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    updated_at = NOW()
WHERE id = 1;

UPDATE usage_dashboard_cost_snapshot
SET aggregation_complete = FALSE,
    computed_at = TIMESTAMPTZ '1970-01-01 00:00:00+00'
WHERE id = 1;
