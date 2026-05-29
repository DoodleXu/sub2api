-- Persist exact subscription bindings for API keys and payment fulfillment.

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS subscription_id BIGINT;

WITH single_active_subscriptions AS (
    SELECT user_id, group_id, MIN(id) AS subscription_id
    FROM user_subscriptions
    WHERE status = 'active'
      AND expires_at > NOW()
      AND deleted_at IS NULL
    GROUP BY user_id, group_id
    HAVING COUNT(*) = 1
)
UPDATE api_keys AS ak
SET subscription_id = sas.subscription_id
FROM single_active_subscriptions AS sas
JOIN groups AS g ON g.id = sas.group_id
WHERE ak.subscription_id IS NULL
  AND ak.deleted_at IS NULL
  AND ak.user_id = sas.user_id
  AND ak.group_id = sas.group_id
  AND g.subscription_type = 'subscription'
  AND g.deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_api_keys_subscription_id
    ON api_keys(subscription_id)
    WHERE deleted_at IS NULL AND subscription_id IS NOT NULL;

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS fulfilled_subscription_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_payment_orders_fulfilled_subscription_id
    ON payment_orders(fulfilled_subscription_id)
    WHERE fulfilled_subscription_id IS NOT NULL;
