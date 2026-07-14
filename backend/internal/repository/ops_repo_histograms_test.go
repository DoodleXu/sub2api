package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetLatencyHistogramAggregatesBoundsBeforeBucketing(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	start := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("WITH bounds AS (")+`(?s:.*)`+regexp.QuoteMeta("CROSS JOIN bounds")).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"min_ms", "max_ms", "total_requests", "bucket_index", "bucket_count",
		}).
			AddRow(1000, 2000, 10, 0, 3).
			AddRow(1000, 2000, 10, 5, 7))

	repo := &opsRepository{db: db}
	got, err := repo.GetLatencyHistogram(context.Background(), &service.OpsDashboardFilter{
		StartTime: start,
		EndTime:   end,
	})
	require.NoError(t, err)
	require.EqualValues(t, 10, got.TotalRequests)
	require.Len(t, got.Buckets, 6)
	require.EqualValues(t, 3, got.Buckets[0].Count)
	require.EqualValues(t, 7, got.Buckets[5].Count)
	require.NoError(t, mock.ExpectationsWereMet())
}
