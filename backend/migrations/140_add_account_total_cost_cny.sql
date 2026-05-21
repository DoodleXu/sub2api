ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS total_cost_cny DECIMAL(18,4) NOT NULL DEFAULT 0;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_total_cost_cny_nonnegative
    CHECK (total_cost_cny >= 0) NOT VALID;
