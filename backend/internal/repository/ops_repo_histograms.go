package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const opsLatencyHistogramQueryTimeout = 5 * time.Second

func (r *opsRepository) GetLatencyHistogram(ctx context.Context, filter *service.OpsDashboardFilter) (*service.OpsLatencyHistogramResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()

	join, where, args, _ := buildUsageWhere(filter, start, end, 1)

	// Keep bounds and bucket aggregation as two indexable range scans. A single
	// WindowAgg scan would have to retain every duration in the selected window
	// and can spill a large custom range to PostgreSQL temporary files.
	q := `
WITH bounds AS (
  SELECT
    MIN(ul.duration_ms::BIGINT) AS min_ms,
    MAX(ul.duration_ms::BIGINT) AS max_ms,
    COUNT(*) AS total_requests
  FROM usage_logs ul
  ` + join + `
  ` + where + `
    AND ul.duration_ms IS NOT NULL
),
bucketed AS (
  SELECT
    bounds.min_ms,
    bounds.max_ms,
    bounds.total_requests,
    CASE
      WHEN bounds.min_ms = bounds.max_ms THEN 0
      WHEN bounds.max_ms - bounds.min_ms + 1 > ` + fmt.Sprint(latencyHistogramLinearSpanThreshold) + ` THEN LEAST(
        FLOOR(
          LN((ul.duration_ms::BIGINT - bounds.min_ms + 1)::DOUBLE PRECISION)
            * ` + fmt.Sprint(latencyHistogramMaxBucketCount) + `::DOUBLE PRECISION
            / LN((bounds.max_ms - bounds.min_ms + 1)::DOUBLE PRECISION)
        )::BIGINT,
        ` + fmt.Sprint(latencyHistogramMaxBucketCount-1) + `::BIGINT
      )
      ELSE LEAST(
        ((ul.duration_ms::BIGINT - bounds.min_ms) * LEAST(` + fmt.Sprint(latencyHistogramMaxBucketCount) + `::BIGINT, bounds.max_ms - bounds.min_ms + 1))
          / (bounds.max_ms - bounds.min_ms + 1),
        LEAST(` + fmt.Sprint(latencyHistogramMaxBucketCount) + `::BIGINT, bounds.max_ms - bounds.min_ms + 1) - 1
      )
    END AS bucket_index
  FROM usage_logs ul
  ` + join + `
  CROSS JOIN bounds
  ` + where + `
    AND ul.duration_ms IS NOT NULL
    AND bounds.total_requests > 0
)
SELECT
  min_ms,
  max_ms,
  total_requests,
  bucket_index,
  COUNT(*) AS bucket_count
FROM bucketed
GROUP BY min_ms, max_ms, total_requests, bucket_index
ORDER BY bucket_index ASC`

	queryCtx, cancel := context.WithTimeout(ctx, opsLatencyHistogramQueryTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(queryCtx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	counts := make(map[int64]int64, latencyHistogramMaxBucketCount)
	var minMs sql.NullInt64
	var maxMs sql.NullInt64
	var total int64
	for rows.Next() {
		var rowMinMs sql.NullInt64
		var rowMaxMs sql.NullInt64
		var bucketIndex sql.NullInt64
		var count int64
		if err := rows.Scan(&rowMinMs, &rowMaxMs, &total, &bucketIndex, &count); err != nil {
			return nil, err
		}
		minMs = rowMinMs
		maxMs = rowMaxMs
		if bucketIndex.Valid {
			counts[bucketIndex.Int64] = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var buckets []*service.OpsLatencyHistogramBucket
	if minMs.Valid && maxMs.Valid {
		buckets = buildDynamicLatencyHistogramBuckets(minMs.Int64, maxMs.Int64, counts)
	}

	return &service.OpsLatencyHistogramResponse{
		StartTime:     start,
		EndTime:       end,
		Platform:      strings.TrimSpace(filter.Platform),
		GroupID:       filter.GroupID,
		TotalRequests: total,
		Buckets:       buckets,
	}, nil
}
