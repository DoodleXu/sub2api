package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryDeleteReliesOnLedgerRebuildTrigger(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)

	mock.ExpectExec("DELETE FROM usage_logs").
		WithArgs(int64(17)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.Delete(context.Background(), 17))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryDeleteNoMatchingUsageIsIdempotent(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)

	mock.ExpectExec("DELETE FROM usage_logs").
		WithArgs(int64(18)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, repo.Delete(context.Background(), 18))
	require.NoError(t, mock.ExpectationsWereMet())
}
