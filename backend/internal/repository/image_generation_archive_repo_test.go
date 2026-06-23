package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationArchiveRepositoryClaimWebConsoleTask(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}
	staleBefore := time.Date(2026, 6, 17, 1, 0, 0, 0, time.UTC)
	now := staleBefore.Add(time.Minute)
	apiKeyID := int64(9)

	rows := sqlmock.NewRows([]string{
		"id", "user_id", "api_key_id", "session_id", "message_id", "status", "request_json", "record_id",
		"error_message", "created_at", "started_at", "completed_at", "user_deleted_at", "updated_at",
	}).AddRow(int64(42), int64(7), apiKeyID, "session-1", "message-1", "running", []byte(`{"version":1}`), nil, "", now, now, nil, nil, now)

	mock.ExpectQuery("UPDATE web_console_image_tasks[\\s\\S]+user_deleted_at IS NULL").
		WithArgs(int64(42), staleBefore).
		WillReturnRows(rows)

	task, claimed, err := repo.ClaimWebConsoleTask(context.Background(), 42, staleBefore)

	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, task)
	require.Equal(t, int64(42), task.ID)
	require.Equal(t, "running", task.Status)
	require.Equal(t, apiKeyID, *task.APIKeyID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationArchiveRepositoryClaimWebConsoleTaskReturnsFalseWhenAlreadyLeased(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}
	staleBefore := time.Date(2026, 6, 17, 1, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{
		"id", "user_id", "api_key_id", "session_id", "message_id", "status", "request_json", "record_id",
		"error_message", "created_at", "started_at", "completed_at", "user_deleted_at", "updated_at",
	})

	mock.ExpectQuery("UPDATE web_console_image_tasks[\\s\\S]+user_deleted_at IS NULL").
		WithArgs(int64(42), staleBefore).
		WillReturnRows(rows)

	task, claimed, err := repo.ClaimWebConsoleTask(context.Background(), 42, staleBefore)

	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, task)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationArchiveRepositoryMarkWebConsoleTasksUserDeletedBySessionID(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}

	mock.ExpectExec("UPDATE web_console_image_tasks").
		WithArgs(int64(7), "session-1").
		WillReturnResult(sqlmock.NewResult(0, 2))

	deleted, err := repo.MarkWebConsoleTasksUserDeletedBySessionID(context.Background(), 7, " session-1 ")

	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationArchiveRepositoryListAllArchiveStorageRefs(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}

	rows := sqlmock.NewRows([]string{"records_deleted", "assets_deleted", "skipped_records", "active_records", "record_ids", "asset_refs"}).
		AddRow(int64(2), int64(1), int64(2), int64(2), []byte(`[11,12]`), []byte(`[{"id":7,"storage_key":"2026/06/image.png","storage_type":"local"}]`))
	mock.ExpectQuery("WHERE status IN \\('completed', 'failed', 'skipped'\\)[\\s\\S]+WHERE status IN \\('pending', 'running'\\)").
		WillReturnRows(rows)

	result, err := repo.ListAllArchiveStorageRefs(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(2), result.RecordsDeleted)
	require.Equal(t, int64(1), result.AssetsDeleted)
	require.Equal(t, int64(2), result.SkippedRecords)
	require.Equal(t, int64(2), result.ActiveRecords)
	require.Equal(t, []int64{11, 12}, result.RecordIDs)
	require.Equal(t, []service.ImageGenerationAssetStorageRef{
		{ID: 7, StorageKey: "2026/06/image.png", StorageType: "local"},
	}, result.AssetRefs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationArchiveRepositoryDeleteArchiveRecordsByID(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}

	rows := sqlmock.NewRows([]string{"count"}).AddRow(int64(2))
	mock.ExpectQuery("DELETE FROM image_generation_records\\s+WHERE id = ANY").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	deleted, err := repo.DeleteArchiveRecordsByID(context.Background(), []int64{11, 12})

	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageGenerationArchiveRepositoryDeleteArchiveRecordsByIDSkipsEmptyPlan(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &imageGenerationArchiveRepository{db: db}

	deleted, err := repo.DeleteArchiveRecordsByID(context.Background(), nil)

	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}
