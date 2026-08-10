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
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "total_account_cost", "total_standard_account_cost"}).AddRow(42, 12.5, 10.0))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	costs, err := repo.loadTotalAccountCosts(context.Background(), []int64{42})
	require.NoError(t, err)
	require.Equal(t, accountCostTotal{totalAccountCost: 12.5, totalStandardAccountCost: 10}, costs[42])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryLoadTotalAccountCostsDoesNotScanUsageDuringBackfill(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_account_cost_totals")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "total_account_cost", "total_standard_account_cost"}))

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

func TestDashboardAggregationRepositoryRefreshDashboardCostSnapshotKeepsPreviousSnapshotWhileLedgerPending(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)BOOL_AND\(initialized AND NOT needs_processing\).*AND l\.complete`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"aggregation_complete"}))

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
