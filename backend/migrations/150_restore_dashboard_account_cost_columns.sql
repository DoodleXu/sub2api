-- Restore account_cost in dashboard aggregate tables so dashboard stats can
-- read account-billed cost from aggregates instead of rescanning usage_logs.

ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

COMMENT ON COLUMN usage_dashboard_hourly.account_cost IS 'SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)) for this dashboard hour.';
COMMENT ON COLUMN usage_dashboard_daily.account_cost IS 'Rolled-up account-billed cost for this dashboard day.';
