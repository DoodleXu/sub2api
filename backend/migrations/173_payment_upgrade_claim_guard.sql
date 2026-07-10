ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS upgrade_claim_active BOOLEAN NOT NULL DEFAULT FALSE;

WITH ranked_claims AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY upgrade_from_subscription_id
               ORDER BY
                   CASE
                       WHEN status IN ('COMPLETED', 'REFUND_REQUESTED', 'REFUNDING', 'REFUND_PENDING', 'PARTIALLY_REFUNDED', 'REFUNDED', 'REFUND_FAILED') THEN 0
                       WHEN paid_at IS NOT NULL THEN 1
                       ELSE 2
                   END,
                   created_at ASC,
                   id ASC
           ) AS claim_rank
    FROM payment_orders
    WHERE upgrade_from_subscription_id IS NOT NULL
      AND status NOT IN ('CANCELLED', 'EXPIRED')
      AND (status <> 'FAILED' OR paid_at IS NOT NULL)
)
UPDATE payment_orders AS payment_order
SET upgrade_claim_active = TRUE
FROM ranked_claims
WHERE payment_order.id = ranked_claims.id
  AND ranked_claims.claim_rank = 1
  AND payment_order.upgrade_claim_active = FALSE;

CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_active_upgrade_claim
    ON payment_orders(upgrade_from_subscription_id)
    WHERE upgrade_from_subscription_id IS NOT NULL
      AND upgrade_claim_active = TRUE;
