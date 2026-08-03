package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDashboardAggregationRecomputeRangeAdvancesModelCoverage(t *testing.T) {
	sqlRecorder := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newDashboardAggregationRepositoryWithSQL(sqlRecorder)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)

	require.NoError(t, repo.RecomputeRange(context.Background(), start, end))
	require.Contains(t, strings.Join(sqlRecorder.execQueries, "\n"), "model_hourly_aggregated_from")
	require.Contains(t, strings.Join(sqlRecorder.execQueries, "\n"), "model_hourly_last_aggregated_at")
}
