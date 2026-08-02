-- Materialized dashboard cost pipeline.
-- Dashboard requests must read these compact tables instead of aggregating usage_logs.

CREATE TABLE IF NOT EXISTS usage_dashboard_account_cost_daily (
    bucket_date DATE NOT NULL,
    account_id BIGINT NOT NULL,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, account_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_daily_account
    ON usage_dashboard_account_cost_daily (account_id, bucket_date DESC);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_account_cost_daily_bucket
    ON usage_dashboard_account_cost_daily (bucket_date DESC);

COMMENT ON TABLE usage_dashboard_account_cost_daily IS 'Pre-aggregated daily account cost used to build the admin dashboard cost snapshot.';

CREATE TABLE IF NOT EXISTS usage_dashboard_model_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    model TEXT NOT NULL,
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, model)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_model_hourly_bucket
    ON usage_dashboard_model_hourly (bucket_start DESC);

CREATE TABLE IF NOT EXISTS usage_dashboard_model_daily (
    bucket_date DATE NOT NULL,
    model TEXT NOT NULL,
    total_requests BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    actual_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, model)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_model_daily_bucket
    ON usage_dashboard_model_daily (bucket_date DESC);

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS model_hourly_aggregated_from TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

ALTER TABLE usage_dashboard_aggregation_watermark
    ADD COLUMN IF NOT EXISTS model_hourly_last_aggregated_at TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';

COMMENT ON COLUMN usage_dashboard_aggregation_watermark.model_hourly_aggregated_from IS 'Earliest time in the contiguous range covered by model dashboard aggregates.';
COMMENT ON COLUMN usage_dashboard_aggregation_watermark.model_hourly_last_aggregated_at IS 'Exclusive end time of the contiguous range covered by model dashboard aggregates.';

CREATE TABLE IF NOT EXISTS usage_dashboard_cost_snapshot (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    today_real_cost_cny DECIMAL(20, 10) NOT NULL DEFAULT 0,
    total_cost_cny DECIMAL(20, 10) NOT NULL DEFAULT 0,
    total_account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    today_account_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    average_cost_cny_per_usd DECIMAL(20, 10) NOT NULL DEFAULT 0,
    anthropic_cost_cny_per_usd DECIMAL(20, 10) NOT NULL DEFAULT 0,
    openai_cost_cny_per_usd DECIMAL(20, 10) NOT NULL DEFAULT 0,
    coverage_start TIMESTAMPTZ NOT NULL,
    coverage_end TIMESTAMPTZ NOT NULL,
    aggregation_complete BOOLEAN NOT NULL DEFAULT FALSE,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE usage_dashboard_cost_snapshot IS 'Single-row materialized cost summary read by admin dashboard requests.';

-- The daily account table and model tables are new materializations. Reset the
-- related watermarks so the background job rebuilds them newest-first instead
-- of publishing a falsely complete snapshot from pre-migration hourly coverage.
UPDATE usage_dashboard_aggregation_watermark
SET account_cost_hourly_aggregated_from = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    account_cost_hourly_last_aggregated_at = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    model_hourly_aggregated_from = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    model_hourly_last_aggregated_at = TIMESTAMPTZ '1970-01-01 00:00:00+00',
    updated_at = NOW()
WHERE id = 1;
