package service

import (
	"context"
	"errors"
	"net/textproto"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type durableBroadcastRepositoryFailureStub struct {
	NotificationEmailBroadcastRepository
	job            NotificationEmailBroadcastJob
	recipient      NotificationEmailBroadcastRecipientRecord
	completeErr    error
	released       bool
	completedState bool
	renewCalls     int
	failRenewAfter int
	waitForCancel  bool
	claimCount     int
}

type durableBroadcastRecipientPageStub struct {
	NotificationEmailBroadcastRepository
	page NotificationEmailBroadcastRecipientPage
}

func (r *durableBroadcastRecipientPageStub) ListRecipients(context.Context, string, string, int, int) (NotificationEmailBroadcastRecipientPage, error) {
	return r.page, nil
}

func (r *durableBroadcastRepositoryFailureStub) AcquireLease(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (r *durableBroadcastRepositoryFailureStub) ReleaseLease(context.Context, string, string) error {
	r.released = true
	return nil
}

func (r *durableBroadcastRepositoryFailureStub) MarkOrphanedSendingUncertain(context.Context, string) error {
	return nil
}

func (r *durableBroadcastRepositoryFailureStub) Get(context.Context, string) (NotificationEmailBroadcastJob, error) {
	return r.job, nil
}

func (r *durableBroadcastRepositoryFailureStub) ListRunnableRecipients(_ context.Context, _ string, afterEmail string, _ int) ([]NotificationEmailBroadcastRecipientRecord, error) {
	if afterEmail != "" {
		return nil, nil
	}
	return []NotificationEmailBroadcastRecipientRecord{r.recipient}, nil
}

func (r *durableBroadcastRepositoryFailureStub) ReserveSendSlot(context.Context, string, string, time.Duration) (time.Time, error) {
	return time.Now(), nil
}

func (r *durableBroadcastRepositoryFailureStub) CancelRequested(context.Context, string) (bool, error) {
	return false, nil
}

func (r *durableBroadcastRepositoryFailureStub) RenewLease(context.Context, string, string, time.Duration) (bool, error) {
	r.renewCalls++
	if r.failRenewAfter > 0 && r.renewCalls >= r.failRenewAfter {
		return false, nil
	}
	return true, nil
}

func (r *durableBroadcastRepositoryFailureStub) ClaimRecipient(context.Context, string, string) (int, bool, error) {
	r.claimCount++
	return 1, true, nil
}

func (r *durableBroadcastRepositoryFailureStub) CompleteRecipient(ctx context.Context, _ string, _ string, _ string, _ string, _ string, _ *time.Time) error {
	if r.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.completeErr
}

func (r *durableBroadcastRepositoryFailureStub) SetJobState(_ context.Context, _ string, status, _ string, _ bool) error {
	if status == "completed" {
		r.completedState = true
	}
	return nil
}

func (r *durableBroadcastRepositoryFailureStub) SetJobStateIfOwned(_ context.Context, _ string, _ string, status, _ string, _ bool) (bool, error) {
	if status == "completed" {
		r.completedState = true
	}
	return true, nil
}

func TestRunDurableBroadcastKeepsJobRecoverableWhenRecipientPersistenceFails(t *testing.T) {
	ctx := context.Background()
	settingRepo := newNotificationEmailMemorySettingRepo()
	email := "recipient@example.test"
	require.NoError(t, settingRepo.Set(ctx, notificationEmailPreferenceKey(NotificationEmailEventAdminBroadcast, email), "unsubscribed"))

	repo := &durableBroadcastRepositoryFailureStub{
		job: NotificationEmailBroadcastJob{BatchID: "broadcast_test", Status: "running", RPM: 30},
		recipient: NotificationEmailBroadcastRecipientRecord{
			BatchID: "broadcast_test", Email: email, NormalizedEmail: email, Status: NotificationEmailBroadcastRecipientPending,
		},
		completeErr: errors.New("database unavailable"),
	}
	svc := NewNotificationEmailService(settingRepo, nil)
	svc.SetBroadcastRepository(repo)

	svc.runDurableBroadcast(ctx, repo.job.BatchID)

	require.True(t, repo.released)
	require.False(t, repo.completedState, "a recipient persistence failure must leave the running job recoverable")
}

func TestRunDurableBroadcastStopsWhenLeaseHeartbeatIsLost(t *testing.T) {
	ctx := context.Background()
	settingRepo := newNotificationEmailMemorySettingRepo()
	email := "recipient@example.test"
	require.NoError(t, settingRepo.Set(ctx, notificationEmailPreferenceKey(NotificationEmailEventAdminBroadcast, email), "unsubscribed"))

	repo := &durableBroadcastRepositoryFailureStub{
		job: NotificationEmailBroadcastJob{BatchID: "broadcast_lease_lost", Status: "running", RPM: 30},
		recipient: NotificationEmailBroadcastRecipientRecord{
			BatchID: "broadcast_lease_lost", Email: email, NormalizedEmail: email, Status: NotificationEmailBroadcastRecipientPending,
		},
		failRenewAfter: 2,
		waitForCancel:  true,
	}
	svc := NewNotificationEmailService(settingRepo, nil)
	svc.SetBroadcastRepository(repo)

	svc.runDurableBroadcastWithLeaseTTL(ctx, repo.job.BatchID, 15*time.Millisecond)

	require.GreaterOrEqual(t, repo.renewCalls, 2)
	require.True(t, repo.released)
	require.False(t, repo.completedState, "a worker that loses its lease must not complete the job")
}

func TestListBroadcastRecipientsMasksAddressesBeforeReturningAPIData(t *testing.T) {
	repo := &durableBroadcastRecipientPageStub{page: NotificationEmailBroadcastRecipientPage{
		Recipients: []NotificationEmailBroadcastRecipientRecord{{
			Email: "person@example.test", NormalizedEmail: "person@example.test", MessageID: "message-1",
		}},
		Total: 1,
	}}
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	svc.SetBroadcastRepository(repo)

	page, err := svc.ListBroadcastRecipients(context.Background(), "batch-1", "", 1, 100)
	require.NoError(t, err)
	require.Equal(t, "p***n@example.test", page.Recipients[0].Email)
	require.Equal(t, "p***n@example.test", page.Recipients[0].NormalizedEmail)
}

func TestResolveBroadcastRecipientsDeduplicatesMailboxDisplayFormats(t *testing.T) {
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	recipients, err := svc.resolveBroadcastRecipients(context.Background(), NotificationEmailBroadcastInput{
		Scope:  "custom",
		Emails: []string{"person@example.test", "Person <person@example.test>"},
	})

	require.NoError(t, err)
	require.Len(t, recipients, 1)
	require.Equal(t, "person@example.test", recipients[0].Email)
}

func TestBroadcastClassifiesUncertainAndTypedErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		code      string
		retry     bool
		uncertain bool
	}{
		{"accepted_but_not_recorded", notificationEmailUncertainDeliveryError{Err: errors.New("database unavailable")}, "delivery_uncertain", false, true},
		{"template", notificationEmailTemplateErr(errors.New("bad template")), "message_template", false, false},
		{"config", notificationEmailConfigErr(errors.New("missing SMTP")), "smtp_configuration", false, false},
		{"temporary_smtp", notificationEmailDeliveryErr(&textproto.Error{Code: 421, Msg: "try later"}), "transient_smtp", true, false},
		{"permanent_smtp", notificationEmailDeliveryErr(&textproto.Error{Code: 550, Msg: "no mailbox"}), "permanent_smtp", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, retry, uncertain := notificationEmailBroadcastClassifyError(tt.err)
			require.Equal(t, tt.code, code)
			require.Equal(t, tt.retry, retry)
			require.Equal(t, tt.uncertain, uncertain)
		})
	}
}

func TestBroadcastDoesNotClaimRecipientBeforeSendSlotIsReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &durableBroadcastRepositoryFailureStub{}
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	svc.SetBroadcastRepository(repo)

	err := svc.sendDurableBroadcastRecipient(ctx,
		NotificationEmailBroadcastJob{BatchID: "batch-1"},
		NotificationEmailBroadcastRecipientRecord{Email: "person@example.com", NormalizedEmail: "person@example.com"},
		"worker-1", time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, repo.claimCount)
}
