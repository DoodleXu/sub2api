//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuditLogRepositoryClearAllIsAtomic(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &auditLogRepository{db: db}
	databaseNow := time.Now().UTC()
	trace := &service.AuditLog{CreatedAt: databaseNow.Add(-time.Hour), Action: service.AuditActionAuditLogClear}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(auditLogWriteAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(databaseNow))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM audit_logs")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(12)))
	mock.ExpectExec(regexp.QuoteMeta("TRUNCATE TABLE audit_logs")).
		WillReturnResult(sqlmock.NewResult(0, 12))
	insertArgs := make([]driver.Value, 16)
	for i := range insertArgs {
		insertArgs[i] = sqlmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(insertArgs...).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	deleted, err := repo.ClearAll(context.Background(), trace)
	require.NoError(t, err)
	require.Equal(t, int64(12), deleted)
	require.Equal(t, databaseNow, trace.CreatedAt)
	require.Equal(t, int64(12), trace.Extra["deleted_rows"])
	require.Equal(t, true, trace.Extra[auditLogClearWatermarkExtraKey])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditLogRepositoryClearAllRollsBackWhenTraceInsertFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &auditLogRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(auditLogWriteAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp"}).AddRow(time.Now().UTC()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM audit_logs")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))
	mock.ExpectExec(regexp.QuoteMeta("TRUNCATE TABLE audit_logs")).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("INSERT INTO audit_logs").WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()

	_, err = repo.ClearAll(context.Background(), &service.AuditLog{})
	require.ErrorContains(t, err, "insert audit clear trace")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditLogRepositoryBatchInsertDropsEntriesQueuedBeforeClear(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &auditLogRepository{db: db}
	cutoff := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(auditLogWriteAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT clock_timestamp\\(\\), MAX\\(created_at\\) FROM audit_logs").
		WithArgs(service.AuditActionAuditLogClear, `{"clear_watermark":true}`).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp", "max"}).AddRow(time.Now().UTC(), cutoff))
	mock.ExpectCommit()

	inserted, err := repo.BatchInsert(context.Background(), []*service.AuditLog{{CreatedAt: cutoff.Add(-time.Second)}})
	require.NoError(t, err)
	require.Zero(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditLogRepositoryBatchInsertFailedClearAttemptDoesNotAdvanceCutoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &auditLogRepository{db: db}
	failedAt := time.Now().UTC().Add(-time.Second)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock($1)")).
		WithArgs(auditLogWriteAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 数据库中即使存在 action=admin.audit_log.clear 的失败 TOTP 记录，显式水位标志
	// 也会把它排除，因此 MAX(created_at) 返回 NULL。
	mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp(), MAX(created_at) FROM audit_logs WHERE action = $1 AND extra @> $2::jsonb")).
		WithArgs(service.AuditActionAuditLogClear, `{"clear_watermark":true}`).
		WillReturnRows(sqlmock.NewRows([]string{"clock_timestamp", "max"}).AddRow(time.Now().UTC(), nil))
	mock.ExpectPrepare(`COPY "audit_logs"`)
	mock.ExpectExec(`COPY "audit_logs"`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`COPY "audit_logs"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	inserted, err := repo.BatchInsert(context.Background(), []*service.AuditLog{{
		CreatedAt:  failedAt,
		Action:     service.AuditActionAuditLogClear,
		StatusCode: 401,
	}})
	require.NoError(t, err)
	require.Equal(t, int64(1), inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}
