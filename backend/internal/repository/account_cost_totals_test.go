package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryLoadTotalAccountCostsReadsOnlyLedger(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_account_cost_totals")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "published_account_cost", "published_standard_account_cost", "published_initialized", "complete"}).AddRow(42, 12.5, 10.0, true, true))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	costs, err := repo.loadTotalAccountCosts(context.Background(), []int64{42})
	require.NoError(t, err)
	require.Equal(t, accountCostTotal{totalAccountCost: 12.5, totalStandardAccountCost: 10, hasPublishedResult: true, complete: true}, costs[42])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryLoadTotalAccountCostsDoesNotScanUsageDuringBackfill(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_account_cost_totals")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "published_account_cost", "published_standard_account_cost", "published_initialized", "complete"}))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	costs, err := repo.loadTotalAccountCosts(context.Background(), []int64{42})
	require.NoError(t, err)
	require.Equal(t, accountCostTotal{}, costs[42])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryProcessAccountCostTotalsAdvancesAccountCheckpointAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id, last_processed_usage_id")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "last_processed_usage_id"}).AddRow(42, 0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_logs")).
		WithArgs(int64(42), int64(0), int64(10000)).
		WillReturnRows(sqlmock.NewRows([]string{"processed_rows"}).AddRow(int64(2)))
	mock.ExpectCommit()

	repo := newDashboardAggregationRepositoryWithSQL(db)
	processed, err := repo.ProcessAccountCostTotals(context.Background(), 10000)
	require.NoError(t, err)
	require.Equal(t, int64(1), processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryProcessAccountCostTotalsPublishesOnlyAfterAccountCatchesUp(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id, last_processed_usage_id")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "last_processed_usage_id"}).AddRow(42, 100))
	mock.ExpectQuery(`(?s)FROM usage_logs.*published_account_cost = CASE.*WHEN totals\.processed_rows < \$3`).
		WithArgs(int64(42), int64(100), int64(10000)).
		WillReturnRows(sqlmock.NewRows([]string{"processed_rows"}).AddRow(int64(10000)))
	mock.ExpectCommit()

	repo := newDashboardAggregationRepositoryWithSQL(db)
	processed, err := repo.ProcessAccountCostTotals(context.Background(), 10000)
	require.NoError(t, err)
	require.Equal(t, int64(1), processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRefreshDashboardCostSnapshotUsesPublishedLedgerWhilePending(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)BOOL_AND\(.*published_account_cost AS total_account_cost.*published_standard_account_cost AS total_standard_account_cost.*ledger_pending`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"aggregation_complete"}).AddRow(true))

	repo := newDashboardAggregationRepositoryWithSQL(db)
	complete, err := repo.RefreshDashboardCostSnapshot(context.Background(), time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, complete)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRefreshDashboardCostSnapshotPublishesPendingState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)BOOL_AND\(.*ledger_pending`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"aggregation_complete"}).AddRow(false))

	repo := newDashboardAggregationRepositoryWithSQL(db)
	complete, err := repo.RefreshDashboardCostSnapshot(context.Background(), time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.False(t, complete)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryMarkDashboardCostSnapshotStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectExec(regexp.QuoteMeta("UPDATE usage_dashboard_cost_snapshot")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newDashboardAggregationRepositoryWithSQL(db)
	require.NoError(t, repo.MarkDashboardCostSnapshotStale(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}
