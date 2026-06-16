package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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
		"error_message", "created_at", "started_at", "completed_at", "updated_at",
	}).AddRow(int64(42), int64(7), apiKeyID, "session-1", "message-1", "running", []byte(`{"version":1}`), nil, "", now, now, nil, now)

	mock.ExpectQuery("UPDATE web_console_image_tasks").
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
		"error_message", "created_at", "started_at", "completed_at", "updated_at",
	})

	mock.ExpectQuery("UPDATE web_console_image_tasks").
		WithArgs(int64(42), staleBefore).
		WillReturnRows(rows)

	task, claimed, err := repo.ClaimWebConsoleTask(context.Background(), 42, staleBefore)

	require.NoError(t, err)
	require.False(t, claimed)
	require.Nil(t, task)
	require.NoError(t, mock.ExpectationsWereMet())
}
