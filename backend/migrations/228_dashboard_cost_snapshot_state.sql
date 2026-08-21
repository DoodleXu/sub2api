ALTER TABLE usage_dashboard_cost_snapshot
    ADD COLUMN IF NOT EXISTS ledger_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS data_through TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS stale_reason TEXT;

COMMENT ON COLUMN usage_dashboard_cost_snapshot.ledger_pending IS
    'A complete published snapshot exists, but the incremental account ledger has pending work.';
COMMENT ON COLUMN usage_dashboard_cost_snapshot.data_through IS
    'Exclusive timestamp through which dashboard aggregation coverage is materialized.';
