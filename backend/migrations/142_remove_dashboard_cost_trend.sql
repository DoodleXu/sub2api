-- Remove dashboard cost-trend storage from deployments that already ran older versions.
-- The dashboard average-cost cards continue to use accounts.total_cost_cny and usage_logs.

DROP TABLE IF EXISTS usage_dashboard_account_cost_daily;
DROP TABLE IF EXISTS usage_dashboard_account_cost_hourly;

ALTER TABLE usage_dashboard_daily DROP COLUMN IF EXISTS account_cost;
ALTER TABLE usage_dashboard_hourly DROP COLUMN IF EXISTS account_cost;
