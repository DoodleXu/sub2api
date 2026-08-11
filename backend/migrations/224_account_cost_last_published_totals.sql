-- Preserve the last complete per-account cost result while a newer ledger
-- calculation is still in progress.
ALTER TABLE usage_account_cost_totals
    ADD COLUMN IF NOT EXISTS published_account_cost NUMERIC NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS published_standard_account_cost NUMERIC NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS published_initialized BOOLEAN NOT NULL DEFAULT FALSE;

-- Only fully caught-up ledgers are safe to publish during migration. Pending
-- ledgers may contain a partial first backfill and must finish once first.
UPDATE usage_account_cost_totals
SET published_account_cost = total_account_cost,
    published_standard_account_cost = total_standard_account_cost,
    published_initialized = TRUE
WHERE initialized
  AND NOT needs_processing
  AND NOT published_initialized;
