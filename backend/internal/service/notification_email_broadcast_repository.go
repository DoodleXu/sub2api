package service

import (
	"context"
	"errors"
	"time"
)

var ErrNotificationEmailBroadcastNotFound = errors.New("notification email broadcast not found")
var ErrNotificationEmailBroadcastAlreadyRunning = errors.New("another email broadcast is already running")

const (
	NotificationEmailBroadcastRecipientPending   = "pending"
	NotificationEmailBroadcastRecipientSending   = "sending"
	NotificationEmailBroadcastRecipientSent      = "sent"
	NotificationEmailBroadcastRecipientSkipped   = "skipped"
	NotificationEmailBroadcastRecipientRetry     = "retry"
	NotificationEmailBroadcastRecipientFailed    = "failed"
	NotificationEmailBroadcastRecipientUncertain = "uncertain"
)

type NotificationEmailBroadcastJob struct {
	BatchID           string
	Status            string
	Scope             string
	Locale            string
	MessageTitle      string
	MessageHTML       string
	ActionLabel       string
	ActionURL         string
	RPM               int
	ContentHash       string
	CreatedByUserID   int64
	CreatedByEmail    string
	TargetCount       int
	SentCount         int
	SkippedCount      int
	UnsubscribedCount int
	FailureCount      int
	UncertainCount    int
	CancelRequested   bool
	LastError         string
	LeaseOwner        string
	LeaseExpiresAt    *time.Time
	StartedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

type NotificationEmailBroadcastRecipientRecord struct {
	BatchID         string     `json:"batch_id"`
	Email           string     `json:"email"`
	NormalizedEmail string     `json:"normalized_email"`
	UserID          int64      `json:"user_id,omitempty"`
	Name            string     `json:"name,omitempty"`
	Locale          string     `json:"locale"`
	Status          string     `json:"status"`
	AttemptCount    int        `json:"attempt_count"`
	ErrorCode       string     `json:"error_code,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	MessageID       string     `json:"message_id"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type NotificationEmailBroadcastRecipientPage struct {
	Recipients []NotificationEmailBroadcastRecipientRecord `json:"recipients"`
	Total      int                                         `json:"total"`
}

type NotificationEmailBroadcastRepository interface {
	Create(ctx context.Context, job NotificationEmailBroadcastJob, recipients []NotificationEmailBroadcastRecipientRecord) error
	Get(ctx context.Context, batchID string) (NotificationEmailBroadcastJob, error)
	List(ctx context.Context, limit, offset int) ([]NotificationEmailBroadcastJob, int, error)
	ListRecipients(ctx context.Context, batchID, status string, limit, offset int) (NotificationEmailBroadcastRecipientPage, error)
	ListRunnableRecipients(ctx context.Context, batchID string) ([]NotificationEmailBroadcastRecipientRecord, error)
	AcquireLease(ctx context.Context, batchID, owner string, ttl time.Duration) (bool, error)
	RenewLease(ctx context.Context, batchID, owner string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, batchID, owner string) error
	MarkOrphanedSendingUncertain(ctx context.Context, batchID string) error
	ClaimRecipient(ctx context.Context, batchID, normalizedEmail string) (int, bool, error)
	CompleteRecipient(ctx context.Context, batchID, normalizedEmail, status, errorCode, lastError string, acceptedAt *time.Time) error
	ResetRecipients(ctx context.Context, batchID, mode string) (int, error)
	SetJobState(ctx context.Context, batchID, status, lastError string, completed bool) error
	SetJobStateIfOwned(ctx context.Context, batchID, owner, status, lastError string, completed bool) (bool, error)
	RequestCancel(ctx context.Context, batchID string) error
	CancelRequested(ctx context.Context, batchID string) (bool, error)
	ListRecoverable(ctx context.Context, limit int) ([]string, error)
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
	GetDraft(ctx context.Context, key string) ([]byte, time.Time, error)
	SaveDraft(ctx context.Context, key string, payload []byte, userID int64) (time.Time, error)
	DeleteDraft(ctx context.Context, key string) error
}
