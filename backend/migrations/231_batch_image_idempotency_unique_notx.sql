-- The public submit path scopes idempotency to one user and API key. Enforce
-- that invariant in PostgreSQL so concurrent submissions cannot both reach a
-- provider. Existing duplicates intentionally make this migration fail: they
-- require operator reconciliation rather than silently deleting paid jobs.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS batch_image_jobs_owner_idempotency_unique_idx
    ON batch_image_jobs (user_id, COALESCE(api_key_id, 0), idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';
