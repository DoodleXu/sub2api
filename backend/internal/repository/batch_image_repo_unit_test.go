//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func newBatchImageRepositoryUnitMock(t *testing.T) (*batchImageRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &batchImageRepository{db: db, sql: db}, mock
}

func TestRecordBatchImageProviderPollFailureSaturatesRetryCount(t *testing.T) {
	repo, mock := newBatchImageRepositoryUnitMock(t)
	mock.ExpectQuery(`UPDATE batch_image_jobs\s+SET last_error_code = \$2,\s+last_error_message = \$3,\s+retry_count = LEAST\(retry_count \+ 1, \$5\),`).
		WithArgs("imgbatch_poll", "UPSTREAM_503", "temporary upstream failure", sqlmock.AnyArg(), 5).
		WillReturnRows(sqlmock.NewRows([]string{"retry_count"}).AddRow(5))
	mock.ExpectExec(`INSERT INTO batch_image_events`).
		WithArgs("imgbatch_poll", "provider_poll_failed", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	retryCount, err := repo.RecordBatchImageProviderPollFailure(
		context.Background(),
		"imgbatch_poll",
		"UPSTREAM_503",
		"temporary upstream failure",
		5,
	)
	require.NoError(t, err)
	require.Equal(t, 5, retryCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMarkBatchImageOutputDeletedPreservesFailedAndCancelledStatus(t *testing.T) {
	repo, mock := newBatchImageRepositoryUnitMock(t)
	deletedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec(`UPDATE batch_image_jobs\s+SET status = CASE WHEN status = 'completed' THEN 'output_deleted' ELSE status END,`).
		WithArgs("imgbatch_cleanup", deletedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO batch_image_events`).
		WithArgs("imgbatch_cleanup", "output_cleanup_completed", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, repo.MarkBatchImageOutputDeleted(context.Background(), "imgbatch_cleanup", deletedAt))
	require.NoError(t, mock.ExpectationsWereMet())
}
