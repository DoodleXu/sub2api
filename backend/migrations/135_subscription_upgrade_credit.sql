ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS upgrade_from_subscription_id BIGINT,
    ADD COLUMN IF NOT EXISTS upgrade_credit_amount DECIMAL(20,2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upgrade_credit_days INT;

CREATE INDEX IF NOT EXISTS idx_payment_orders_upgrade_from_subscription_id
    ON payment_orders(upgrade_from_subscription_id);
