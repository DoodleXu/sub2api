package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNotificationEmailBroadcastCompleteRecipientRejectsInvalidSourceState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &notificationEmailBroadcastRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status FROM notification_email_broadcast_recipients").
		WithArgs("batch-1", "person@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(service.NotificationEmailBroadcastRecipientPending))
	mock.ExpectRollback()

	err = repo.CompleteRecipient(context.Background(), "batch-1", "Person@Example.com", service.NotificationEmailBroadcastRecipientSent, "", "", nil)
	require.ErrorContains(t, err, "invalid email broadcast recipient transition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNotificationEmailBroadcastCompleteRecipientRejectsUnknownTargetState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &notificationEmailBroadcastRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status FROM notification_email_broadcast_recipients").
		WithArgs("batch-1", "person@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(service.NotificationEmailBroadcastRecipientSending))
	mock.ExpectRollback()

	err = repo.CompleteRecipient(context.Background(), "batch-1", "person@example.com", "completed", "", "", nil)
	require.ErrorContains(t, err, "invalid email broadcast recipient status")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNotificationEmailBroadcastSetJobStateIfOwnedDoesNotCompleteAfterLeaseLoss(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &notificationEmailBroadcastRepository{db: db}

	mock.ExpectExec("UPDATE notification_email_broadcast_jobs SET status=\\$3.*cancel_requested=FALSE").
		WithArgs("batch-1", "worker-1", "completed", "").
		WillReturnResult(sqlmock.NewResult(0, 0))

	updated, err := repo.SetJobStateIfOwned(context.Background(), "batch-1", "worker-1", "completed", "", true)
	require.NoError(t, err)
	require.False(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNotificationEmailBroadcastResetRecipientsFailedOnlyResetsFailedRecipients(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &notificationEmailBroadcastRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE notification_email_broadcast_recipients SET status='retry'.*status='failed'").
		WithArgs("batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE notification_email_broadcast_jobs j SET").
		WithArgs("batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE notification_email_broadcast_jobs SET status='running'.*status NOT IN").
		WithArgs("batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	count, err := repo.ResetRecipients(context.Background(), "batch-1", "failed")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, mock.ExpectationsWereMet())
}
