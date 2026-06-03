-- 148_add_account_archived_at.sql
-- Add account archive marker independent from status. Archived accounts keep
-- usage/cost history but are hidden from default account management views and
-- excluded from scheduling.

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_accounts_archived_at
    ON accounts(archived_at)
    WHERE deleted_at IS NULL;

COMMENT ON COLUMN accounts.archived_at IS 'Archive timestamp. NULL means visible/active lifecycle; non-NULL keeps history but hides from default account management and scheduling.';
