-- Apply single-row historical usage corrections as ledger deltas whenever the
-- account ledger is fully caught up. Only ambiguous partial-ledger states fall
-- back to a full account rebuild.
CREATE TABLE IF NOT EXISTS usage_account_cost_dirty_buckets (
    bucket_start TIMESTAMPTZ PRIMARY KEY,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_usage_account_cost_dirty_buckets_requested
    ON usage_account_cost_dirty_buckets (requested_at, bucket_start);

CREATE OR REPLACE FUNCTION mark_account_cost_dirty_bucket(bucket_time TIMESTAMPTZ)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO usage_account_cost_dirty_buckets (bucket_start)
    -- The application DSN pins PostgreSQL TimeZone to the configured business
    -- timezone. Keep the conversion explicit so this key matches the hourly
    -- rollup expressions even when the server/session default is UTC.
    VALUES (date_trunc('hour', bucket_time AT TIME ZONE current_setting('TimeZone')) AT TIME ZONE current_setting('TimeZone'))
    ON CONFLICT (bucket_start) DO UPDATE SET requested_at = NOW();
END;
$$;

CREATE OR REPLACE FUNCTION apply_account_cost_delta(
    target_account_id BIGINT,
    usage_id BIGINT,
    account_cost_delta NUMERIC,
    standard_cost_delta NUMERIC,
    usage_created_at TIMESTAMPTZ
) RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    changed_rows INTEGER;
BEGIN
    UPDATE usage_account_cost_totals
    SET total_account_cost = total_account_cost + account_cost_delta,
        total_standard_account_cost = total_standard_account_cost + standard_cost_delta,
        published_account_cost = published_account_cost + account_cost_delta,
        published_standard_account_cost = published_standard_account_cost + standard_cost_delta,
        computed_at = NOW()
    WHERE account_id = target_account_id
      AND initialized
      AND NOT needs_processing
      AND published_initialized
      AND usage_id <= last_processed_usage_id;
    GET DIAGNOSTICS changed_rows = ROW_COUNT;
    IF changed_rows > 0 THEN
        PERFORM mark_account_cost_dirty_bucket(usage_created_at);
        RETURN;
    END IF;

    UPDATE usage_account_cost_totals
    SET needs_processing = TRUE
    WHERE account_id = target_account_id
      AND initialized
      AND usage_id > last_processed_usage_id;
    GET DIAGNOSTICS changed_rows = ROW_COUNT;
    IF changed_rows > 0 THEN
        PERFORM mark_account_cost_dirty_bucket(usage_created_at);
        RETURN;
    END IF;

    INSERT INTO usage_account_cost_totals (
        account_id, total_account_cost, total_standard_account_cost,
        last_processed_usage_id, initialized, needs_processing, computed_at
    )
    SELECT target_account_id, 0, 0, 0, FALSE, TRUE, NOW()
    FROM accounts
    WHERE id = target_account_id
    ON CONFLICT (account_id) DO UPDATE
    SET total_account_cost = 0,
        total_standard_account_cost = 0,
        last_processed_usage_id = 0,
        initialized = FALSE,
        needs_processing = TRUE,
        computed_at = NOW();
    PERFORM mark_account_cost_dirty_bucket(usage_created_at);
END;
$$;

CREATE OR REPLACE FUNCTION mark_account_cost_total_pending()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO usage_account_cost_totals (account_id, needs_processing)
    VALUES (NEW.account_id, TRUE)
    ON CONFLICT (account_id) DO NOTHING;

    UPDATE usage_account_cost_totals
    SET total_account_cost = CASE WHEN NEW.id <= last_processed_usage_id THEN 0 ELSE total_account_cost END,
        total_standard_account_cost = CASE WHEN NEW.id <= last_processed_usage_id THEN 0 ELSE total_standard_account_cost END,
        last_processed_usage_id = CASE WHEN NEW.id <= last_processed_usage_id THEN 0 ELSE last_processed_usage_id END,
        initialized = CASE WHEN NEW.id <= last_processed_usage_id THEN FALSE ELSE initialized END,
        needs_processing = TRUE,
        computed_at = CASE WHEN NEW.id <= last_processed_usage_id THEN NOW() ELSE computed_at END
    WHERE account_id = NEW.account_id;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION reset_account_cost_total_for_usage_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    old_account_cost NUMERIC;
    new_account_cost NUMERIC;
BEGIN
    old_account_cost := COALESCE(OLD.account_stats_cost, OLD.total_cost) * COALESCE(OLD.account_rate_multiplier, 1);

    IF TG_OP = 'DELETE' THEN
        PERFORM apply_account_cost_delta(OLD.account_id, OLD.id, -old_account_cost, -OLD.total_cost, OLD.created_at);
        RETURN OLD;
    END IF;

    new_account_cost := COALESCE(NEW.account_stats_cost, NEW.total_cost) * COALESCE(NEW.account_rate_multiplier, 1);
    IF OLD.account_id = NEW.account_id THEN
        PERFORM apply_account_cost_delta(
            NEW.account_id,
            NEW.id,
            new_account_cost - old_account_cost,
            NEW.total_cost - OLD.total_cost,
            NEW.created_at
        );
    ELSE
        PERFORM apply_account_cost_delta(OLD.account_id, OLD.id, -old_account_cost, -OLD.total_cost, OLD.created_at);
        PERFORM apply_account_cost_delta(NEW.account_id, NEW.id, new_account_cost, NEW.total_cost, NEW.created_at);
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION initialize_account_cost_total()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO usage_account_cost_totals (
        account_id, initialized, needs_processing,
        published_initialized, computed_at
    ) VALUES (NEW.id, TRUE, FALSE, TRUE, NOW())
    ON CONFLICT (account_id) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_accounts_cost_total_initialize ON accounts;
CREATE TRIGGER trg_accounts_cost_total_initialize
AFTER INSERT ON accounts
FOR EACH ROW
EXECUTE FUNCTION initialize_account_cost_total();
