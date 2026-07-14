-- Persist the image-generation subset of TTFT for Ops dashboard pre-aggregation.
--
-- Only streaming image requests with a recorded first_token_ms contribute.
-- Video generations are excluded even though legacy video billing rows may also
-- carry image_count=1.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_metrics_hourly
    ADD COLUMN IF NOT EXISTS image_generation_ttft_sample_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS image_generation_ttft_avg_ms DOUBLE PRECISION;

ALTER TABLE ops_metrics_daily
    ADD COLUMN IF NOT EXISTS image_generation_ttft_sample_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS image_generation_ttft_avg_ms DOUBLE PRECISION;

-- Existing pre-aggregated rows predate the columns above. Backfill only the
-- time span that is still present in ops_metrics_hourly, so migration cost is
-- bounded by the configured Ops retention window instead of table lifetime.
WITH hourly_bounds AS (
    SELECT
        MIN(bucket_start) AS start_time,
        MAX(bucket_start) + INTERVAL '1 hour' AS end_time
    FROM ops_metrics_hourly
),
image_usage_base AS (
    SELECT
        date_trunc('hour', ul.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
        g.platform AS platform,
        ul.group_id AS group_id,
        ul.first_token_ms AS first_token_ms
    FROM usage_logs ul
    JOIN groups g ON g.id = ul.group_id
    CROSS JOIN hourly_bounds bounds
    WHERE bounds.start_time IS NOT NULL
      AND ul.created_at >= bounds.start_time
      AND ul.created_at < bounds.end_time
      AND ul.first_token_ms IS NOT NULL
      AND ul.image_count > 0
      AND COALESCE(ul.video_count, 0) = 0
),
image_usage_agg AS (
    SELECT
        bucket_start,
        CASE WHEN GROUPING(platform) = 1 THEN NULL ELSE platform END AS platform,
        CASE WHEN GROUPING(group_id) = 1 THEN NULL ELSE group_id END AS group_id,
        COUNT(*) AS sample_count,
        AVG(first_token_ms) AS avg_ms
    FROM image_usage_base
    GROUP BY GROUPING SETS (
        (bucket_start),
        (bucket_start, platform),
        (bucket_start, platform, group_id)
    )
),
hourly_backfill AS (
    SELECT
        hourly.id,
        COALESCE(agg.sample_count, 0) AS sample_count,
        agg.avg_ms
    FROM ops_metrics_hourly hourly
    LEFT JOIN image_usage_agg agg
      ON hourly.bucket_start = agg.bucket_start
     AND COALESCE(hourly.platform, '') = COALESCE(agg.platform, '')
     AND COALESCE(hourly.group_id, 0) = COALESCE(agg.group_id, 0)
)
UPDATE ops_metrics_hourly hourly
SET
    image_generation_ttft_sample_count = backfill.sample_count,
    image_generation_ttft_avg_ms = backfill.avg_ms
FROM hourly_backfill backfill
WHERE hourly.id = backfill.id
  AND (
      hourly.image_generation_ttft_sample_count IS DISTINCT FROM backfill.sample_count
      OR hourly.image_generation_ttft_avg_ms IS DISTINCT FROM backfill.avg_ms
  );

-- Rebuild the daily subset from the now-correct hourly values. Rows without
-- image samples intentionally keep the 0/NULL defaults.
WITH image_daily_agg AS (
    SELECT
        (bucket_start AT TIME ZONE 'UTC')::date AS bucket_date,
        platform,
        group_id,
        SUM(image_generation_ttft_sample_count) AS sample_count,
        SUM(image_generation_ttft_avg_ms * image_generation_ttft_sample_count)
            / NULLIF(SUM(image_generation_ttft_sample_count), 0) AS avg_ms
    FROM ops_metrics_hourly
    WHERE image_generation_ttft_sample_count > 0
      AND image_generation_ttft_avg_ms IS NOT NULL
    GROUP BY 1, 2, 3
),
daily_backfill AS (
    SELECT
        daily.id,
        COALESCE(agg.sample_count, 0) AS sample_count,
        agg.avg_ms
    FROM ops_metrics_daily daily
    LEFT JOIN image_daily_agg agg
      ON daily.bucket_date = agg.bucket_date
     AND COALESCE(daily.platform, '') = COALESCE(agg.platform, '')
     AND COALESCE(daily.group_id, 0) = COALESCE(agg.group_id, 0)
)
UPDATE ops_metrics_daily daily
SET
    image_generation_ttft_sample_count = backfill.sample_count,
    image_generation_ttft_avg_ms = backfill.avg_ms
FROM daily_backfill backfill
WHERE daily.id = backfill.id
  AND (
      daily.image_generation_ttft_sample_count IS DISTINCT FROM backfill.sample_count
      OR daily.image_generation_ttft_avg_ms IS DISTINCT FROM backfill.avg_ms
  );
