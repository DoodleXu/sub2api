-- Keep API Key revenue and account cost in the official dashboard aggregates.
ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS api_key_actual_cost DECIMAL(20, 10),
    ADD COLUMN IF NOT EXISTS api_key_account_cost DECIMAL(20, 10);

ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS api_key_actual_cost DECIMAL(20, 10),
    ADD COLUMN IF NOT EXISTS api_key_account_cost DECIMAL(20, 10);

COMMENT ON COLUMN usage_dashboard_hourly.api_key_actual_cost IS 'API Key usage actual cost for this dashboard hour.';
COMMENT ON COLUMN usage_dashboard_hourly.api_key_account_cost IS 'API Key account-billed cost for this dashboard hour.';
COMMENT ON COLUMN usage_dashboard_daily.api_key_actual_cost IS 'API Key usage actual cost for this dashboard day.';
COMMENT ON COLUMN usage_dashboard_daily.api_key_account_cost IS 'API Key account-billed cost for this dashboard day.';

-- Force the aggregation worker to rebuild its retained window with the new
-- columns. Older raw logs may already have been cleaned up and cannot be
-- reconstructed by this migration.
UPDATE usage_dashboard_aggregation_watermark
SET last_aggregated_at = TIMESTAMPTZ '1970-01-01 00:00:00+00', updated_at = NOW()
WHERE id = 1;
