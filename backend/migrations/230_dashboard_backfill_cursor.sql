ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS dashboard_backfill_cursor TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dashboard_backfill_target TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dashboard_backfill_status TEXT NOT NULL DEFAULT 'idle';

COMMENT ON COLUMN usage_dashboard_aggregation_watermark.dashboard_backfill_cursor IS
    'Next bounded timestamp for startup dashboard backfill; NULL when no historical backfill is active.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.dashboard_backfill_target IS
    'Exclusive target timestamp for startup dashboard backfill.';
