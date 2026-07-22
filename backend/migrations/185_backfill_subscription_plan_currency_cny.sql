-- This fork historically displays subscription plan prices in CNY. Migration 177
-- introduced the display currency with an empty legacy default, so backfill
-- existing plans without changing explicitly configured currencies.
UPDATE subscription_plans
SET currency = 'CNY'
WHERE TRIM(currency) = '';

ALTER TABLE subscription_plans
    ALTER COLUMN currency SET DEFAULT 'CNY';
