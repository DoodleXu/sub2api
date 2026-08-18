package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const notificationEmailBroadcastDraftKey = "admin"

type notificationEmailBroadcastRepository struct{ db *sql.DB }

func NewNotificationEmailBroadcastRepository(db *sql.DB) service.NotificationEmailBroadcastRepository {
	return &notificationEmailBroadcastRepository{db: db}
}

func (r *notificationEmailBroadcastRepository) unavailable() error {
	if r == nil || r.db == nil {
		return errors.New("notification email broadcast database is unavailable")
	}
	return nil
}

func (r *notificationEmailBroadcastRepository) Create(ctx context.Context, job service.NotificationEmailBroadcastJob, recipients []service.NotificationEmailBroadcastRecipientRecord) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin email broadcast create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO notification_email_broadcast_jobs
		(batch_id,status,scope,locale,message_title,message_html,action_label,action_url,rpm,content_hash,created_by_user_id,created_by_email,target_count,started_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),$9,$10,NULLIF($11,0),NULLIF($12,''),$13,$14,$14)
	`, job.BatchID, job.Status, job.Scope, job.Locale, job.MessageTitle, job.MessageHTML,
		job.ActionLabel, job.ActionURL, job.RPM, job.ContentHash, job.CreatedByUserID, job.CreatedByEmail,
		len(recipients), job.StartedAt)
	if err != nil {
		if isNotificationEmailBroadcastActiveConflict(err) {
			return service.ErrNotificationEmailBroadcastAlreadyRunning
		}
		return fmt.Errorf("insert email broadcast job: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO notification_email_broadcast_recipients
		(batch_id,email,normalized_email,user_id,recipient_name,locale,status,message_id)
		VALUES ($1,$2,$3,NULLIF($4,0),NULLIF($5,''),$6,$7,$8)
	`)
	if err != nil {
		return fmt.Errorf("prepare email broadcast recipients: %w", err)
	}
	defer stmt.Close()
	for _, recipient := range recipients {
		if _, err := stmt.ExecContext(ctx, job.BatchID, recipient.Email, recipient.NormalizedEmail, recipient.UserID, recipient.Name,
			recipient.Locale, recipient.Status, recipient.MessageID); err != nil {
			return fmt.Errorf("insert email broadcast recipient: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit email broadcast create: %w", err)
	}
	return nil
}

func (r *notificationEmailBroadcastRepository) Get(ctx context.Context, batchID string) (service.NotificationEmailBroadcastJob, error) {
	if err := r.unavailable(); err != nil {
		return service.NotificationEmailBroadcastJob{}, err
	}
	var job service.NotificationEmailBroadcastJob
	var createdByID sql.NullInt64
	var createdByEmail, lastError, leaseOwner sql.NullString
	var leaseExpires, completed sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT batch_id,status,scope,locale,message_title,message_html,COALESCE(action_label,''),COALESCE(action_url,''),rpm,content_hash,
		       created_by_user_id,created_by_email,target_count,sent_count,skipped_count,unsubscribed_count,failure_count,uncertain_count,
		       cancel_requested,last_error,lease_owner,lease_expires_at,started_at,updated_at,completed_at
		FROM notification_email_broadcast_jobs WHERE batch_id=$1
	`, batchID).Scan(&job.BatchID, &job.Status, &job.Scope, &job.Locale, &job.MessageTitle, &job.MessageHTML, &job.ActionLabel, &job.ActionURL,
		&job.RPM, &job.ContentHash, &createdByID, &createdByEmail, &job.TargetCount, &job.SentCount, &job.SkippedCount, &job.UnsubscribedCount,
		&job.FailureCount, &job.UncertainCount, &job.CancelRequested, &lastError, &leaseOwner, &leaseExpires, &job.StartedAt, &job.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return service.NotificationEmailBroadcastJob{}, service.ErrNotificationEmailBroadcastNotFound
	}
	if err != nil {
		return service.NotificationEmailBroadcastJob{}, fmt.Errorf("get email broadcast job: %w", err)
	}
	if createdByID.Valid {
		job.CreatedByUserID = createdByID.Int64
	}
	job.CreatedByEmail, job.LastError, job.LeaseOwner = createdByEmail.String, lastError.String, leaseOwner.String
	if leaseExpires.Valid {
		t := leaseExpires.Time
		job.LeaseExpiresAt = &t
	}
	if completed.Valid {
		t := completed.Time
		job.CompletedAt = &t
	}
	return job, nil
}

func (r *notificationEmailBroadcastRepository) List(ctx context.Context, limit, offset int) ([]service.NotificationEmailBroadcastJob, int, error) {
	if err := r.unavailable(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_email_broadcast_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT batch_id FROM notification_email_broadcast_jobs ORDER BY updated_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	jobs := make([]service.NotificationEmailBroadcastJob, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		job, err := r.Get(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, job)
	}
	return jobs, total, rows.Err()
}

func (r *notificationEmailBroadcastRepository) ListRecipients(ctx context.Context, batchID, status string, limit, offset int) (service.NotificationEmailBroadcastRecipientPage, error) {
	if err := r.unavailable(); err != nil {
		return service.NotificationEmailBroadcastRecipientPage{}, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	args := []any{batchID}
	where := "batch_id=$1"
	if strings.TrimSpace(status) != "" {
		where += " AND status=$2"
		args = append(args, status)
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_email_broadcast_recipients WHERE "+where, args...).Scan(&total); err != nil {
		return service.NotificationEmailBroadcastRecipientPage{}, err
	}
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, `
		SELECT batch_id,email,normalized_email,COALESCE(user_id,0),COALESCE(recipient_name,''),locale,status,attempt_count,
		       COALESCE(error_code,''),COALESCE(last_error,''),message_id,accepted_at,updated_at
		FROM notification_email_broadcast_recipients WHERE `+where+` ORDER BY normalized_email LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return service.NotificationEmailBroadcastRecipientPage{}, err
	}
	defer rows.Close()
	page := service.NotificationEmailBroadcastRecipientPage{Recipients: make([]service.NotificationEmailBroadcastRecipientRecord, 0, limit), Total: total}
	for rows.Next() {
		var item service.NotificationEmailBroadcastRecipientRecord
		var accepted sql.NullTime
		if err := rows.Scan(&item.BatchID, &item.Email, &item.NormalizedEmail, &item.UserID, &item.Name, &item.Locale, &item.Status, &item.AttemptCount, &item.ErrorCode, &item.LastError, &item.MessageID, &accepted, &item.UpdatedAt); err != nil {
			return service.NotificationEmailBroadcastRecipientPage{}, err
		}
		if accepted.Valid {
			t := accepted.Time
			item.AcceptedAt = &t
		}
		page.Recipients = append(page.Recipients, item)
	}
	return page, rows.Err()
}

func (r *notificationEmailBroadcastRepository) ListRunnableRecipients(ctx context.Context, batchID string) ([]service.NotificationEmailBroadcastRecipientRecord, error) {
	if err := r.unavailable(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT batch_id,email,normalized_email,COALESCE(user_id,0),COALESCE(recipient_name,''),locale,status,attempt_count,COALESCE(error_code,''),COALESCE(last_error,''),message_id,accepted_at,updated_at FROM notification_email_broadcast_recipients WHERE batch_id=$1 AND status IN ('pending','retry') ORDER BY normalized_email`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.NotificationEmailBroadcastRecipientRecord, 0)
	for rows.Next() {
		var item service.NotificationEmailBroadcastRecipientRecord
		var accepted sql.NullTime
		if err := rows.Scan(&item.BatchID, &item.Email, &item.NormalizedEmail, &item.UserID, &item.Name, &item.Locale, &item.Status, &item.AttemptCount, &item.ErrorCode, &item.LastError, &item.MessageID, &accepted, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if accepted.Valid {
			t := accepted.Time
			item.AcceptedAt = &t
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *notificationEmailBroadcastRepository) AcquireLease(ctx context.Context, batchID, owner string, ttl time.Duration) (bool, error) {
	if err := r.unavailable(); err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET lease_owner=$2, lease_expires_at=NOW()+($3 * INTERVAL '1 second'), updated_at=NOW() WHERE batch_id=$1 AND status IN ('running','canceling') AND (lease_owner IS NULL OR lease_expires_at < NOW() OR lease_owner=$2)`, batchID, owner, int64(ttl/time.Second))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *notificationEmailBroadcastRepository) RenewLease(ctx context.Context, batchID, owner string, ttl time.Duration) (bool, error) {
	if err := r.unavailable(); err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET lease_expires_at=NOW()+($3 * INTERVAL '1 second'), updated_at=NOW() WHERE batch_id=$1 AND lease_owner=$2 AND status IN ('running','canceling')`, batchID, owner, int64(ttl/time.Second))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *notificationEmailBroadcastRepository) ReleaseLease(ctx context.Context, batchID, owner string) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET lease_owner=NULL,lease_expires_at=NULL WHERE batch_id=$1 AND lease_owner=$2`, batchID, owner)
	return err
}

func (r *notificationEmailBroadcastRepository) MarkOrphanedSendingUncertain(ctx context.Context, batchID string) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `WITH changed AS (UPDATE notification_email_broadcast_recipients SET status='uncertain',error_code='worker_lost',last_error='worker stopped after SMTP attempt; delivery result is unknown',updated_at=NOW() WHERE batch_id=$1 AND status='sending' RETURNING 1) UPDATE notification_email_broadcast_jobs SET uncertain_count=uncertain_count+(SELECT COUNT(*) FROM changed),updated_at=NOW() WHERE batch_id=$1`, batchID)
	return err
}

func (r *notificationEmailBroadcastRepository) ClaimRecipient(ctx context.Context, batchID, normalizedEmail string) (int, bool, error) {
	if err := r.unavailable(); err != nil {
		return 0, false, err
	}
	var attempt int
	err := r.db.QueryRowContext(ctx, `UPDATE notification_email_broadcast_recipients SET status='sending',attempt_count=attempt_count+1,updated_at=NOW() WHERE batch_id=$1 AND normalized_email=$2 AND status IN ('pending','retry') RETURNING attempt_count`, batchID, strings.ToLower(strings.TrimSpace(normalizedEmail))).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return attempt, err == nil, err
}

func (r *notificationEmailBroadcastRepository) CompleteRecipient(ctx context.Context, batchID, normalizedEmail, status, errorCode, lastError string, acceptedAt *time.Time) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previous string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM notification_email_broadcast_recipients WHERE batch_id=$1 AND normalized_email=$2 FOR UPDATE`, batchID, strings.ToLower(strings.TrimSpace(normalizedEmail))).Scan(&previous); err != nil {
		return err
	}
	if previous == service.NotificationEmailBroadcastRecipientSent || previous == service.NotificationEmailBroadcastRecipientSkipped {
		return tx.Commit()
	}
	if previous != service.NotificationEmailBroadcastRecipientSending {
		return fmt.Errorf("invalid email broadcast recipient transition from %q to %q", previous, status)
	}
	switch status {
	case service.NotificationEmailBroadcastRecipientSent,
		service.NotificationEmailBroadcastRecipientSkipped,
		service.NotificationEmailBroadcastRecipientRetry,
		service.NotificationEmailBroadcastRecipientFailed:
	default:
		return fmt.Errorf("invalid email broadcast recipient status %q", status)
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_email_broadcast_recipients SET status=$3,error_code=NULLIF($4,''),last_error=NULLIF($5,''),accepted_at=COALESCE($6,accepted_at),updated_at=NOW() WHERE batch_id=$1 AND normalized_email=$2`, batchID, strings.ToLower(strings.TrimSpace(normalizedEmail)), status, errorCode, lastError, acceptedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE notification_email_broadcast_jobs j SET
			sent_count = counts.sent_count,
			skipped_count = counts.skipped_count,
			unsubscribed_count = counts.unsubscribed_count,
			failure_count = counts.failure_count,
			uncertain_count = counts.uncertain_count,
			updated_at = NOW()
		FROM (
			SELECT
				COUNT(*) FILTER (WHERE status='sent')::INTEGER AS sent_count,
				COUNT(*) FILTER (WHERE status='skipped')::INTEGER AS skipped_count,
				COUNT(*) FILTER (WHERE status='skipped' AND error_code='unsubscribed')::INTEGER AS unsubscribed_count,
				COUNT(*) FILTER (WHERE status='failed')::INTEGER AS failure_count,
				COUNT(*) FILTER (WHERE status='uncertain')::INTEGER AS uncertain_count
			FROM notification_email_broadcast_recipients WHERE batch_id=$1
		) counts WHERE j.batch_id=$1
	`, batchID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *notificationEmailBroadcastRepository) ResetRecipients(ctx context.Context, batchID, mode string) (int, error) {
	if err := r.unavailable(); err != nil {
		return 0, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var where string
	switch mode {
	case "remaining":
		where = "status IN ('pending','retry','failed')"
	case "failed":
		where = "status='failed'"
	case "uncertain":
		where = "status='uncertain'"
	default:
		return 0, fmt.Errorf("unsupported broadcast resume mode: %s", mode)
	}
	result, err := tx.ExecContext(ctx, `UPDATE notification_email_broadcast_recipients SET status='retry',error_code=NULL,last_error=NULL,updated_at=NOW() WHERE batch_id=$1 AND `+where, batchID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("broadcast has no recipients to send")
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE notification_email_broadcast_jobs j SET
			sent_count = counts.sent_count,
			skipped_count = counts.skipped_count,
			unsubscribed_count = counts.unsubscribed_count,
			failure_count = counts.failure_count,
			uncertain_count = counts.uncertain_count,
			updated_at = NOW()
		FROM (
			SELECT
				COUNT(*) FILTER (WHERE status='sent')::INTEGER AS sent_count,
				COUNT(*) FILTER (WHERE status='skipped')::INTEGER AS skipped_count,
				COUNT(*) FILTER (WHERE status='skipped' AND error_code='unsubscribed')::INTEGER AS unsubscribed_count,
				COUNT(*) FILTER (WHERE status='failed')::INTEGER AS failure_count,
				COUNT(*) FILTER (WHERE status='uncertain')::INTEGER AS uncertain_count
			FROM notification_email_broadcast_recipients WHERE batch_id=$1
		) counts WHERE j.batch_id=$1
	`, batchID)
	if err != nil {
		return 0, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET status='running',last_error=NULL,cancel_requested=FALSE,completed_at=NULL,lease_owner=NULL,lease_expires_at=NULL,updated_at=NOW() WHERE batch_id=$1 AND status NOT IN ('running','canceling')`, batchID)
	if err != nil {
		if isNotificationEmailBroadcastActiveConflict(err) {
			return 0, service.ErrNotificationEmailBroadcastAlreadyRunning
		}
		return 0, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if updated == 0 {
		return 0, service.ErrNotificationEmailBroadcastAlreadyRunning
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

func isNotificationEmailBroadcastActiveConflict(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "idx_notification_email_broadcast_one_active"
}

func (r *notificationEmailBroadcastRepository) SetJobState(ctx context.Context, batchID, status, lastError string, completed bool) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	if completed {
		_, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET status=$2,last_error=NULLIF($3,''),completed_at=NOW(),lease_owner=NULL,lease_expires_at=NULL,updated_at=NOW() WHERE batch_id=$1`, batchID, status, lastError)
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET status=$2,last_error=NULLIF($3,''),cancel_requested=FALSE,completed_at=NULL,updated_at=NOW() WHERE batch_id=$1`, batchID, status, lastError)
	return err
}

func (r *notificationEmailBroadcastRepository) SetJobStateIfOwned(ctx context.Context, batchID, owner, status, lastError string, completed bool) (bool, error) {
	if err := r.unavailable(); err != nil {
		return false, err
	}
	var (
		result sql.Result
		err    error
	)
	if completed {
		where := "batch_id=$1 AND lease_owner=$2 AND lease_expires_at>=NOW()"
		if status == "completed" {
			where += " AND status='running' AND cancel_requested=FALSE"
		}
		result, err = r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET status=$3,last_error=NULLIF($4,''),completed_at=NOW(),lease_owner=NULL,lease_expires_at=NULL,updated_at=NOW() WHERE `+where, batchID, owner, status, lastError)
	} else {
		result, err = r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET status=$3,last_error=NULLIF($4,''),cancel_requested=FALSE,completed_at=NULL,updated_at=NOW() WHERE batch_id=$1 AND lease_owner=$2 AND lease_expires_at>=NOW()`, batchID, owner, status, lastError)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (r *notificationEmailBroadcastRepository) RequestCancel(ctx context.Context, batchID string) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE notification_email_broadcast_jobs SET cancel_requested=TRUE,status=CASE WHEN status='running' THEN 'canceling' ELSE status END,updated_at=NOW() WHERE batch_id=$1`, batchID)
	return err
}
func (r *notificationEmailBroadcastRepository) CancelRequested(ctx context.Context, batchID string) (bool, error) {
	if err := r.unavailable(); err != nil {
		return false, err
	}
	var v bool
	err := r.db.QueryRowContext(ctx, `SELECT cancel_requested FROM notification_email_broadcast_jobs WHERE batch_id=$1`, batchID).Scan(&v)
	return v, err
}
func (r *notificationEmailBroadcastRepository) ListRecoverable(ctx context.Context, limit int) ([]string, error) {
	if err := r.unavailable(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `SELECT batch_id FROM notification_email_broadcast_jobs WHERE status IN ('running','canceling') AND (lease_expires_at IS NULL OR lease_expires_at<NOW()) ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *notificationEmailBroadcastRepository) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	if err := r.unavailable(); err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM notification_email_broadcast_jobs WHERE updated_at<$1 AND status IN ('completed','canceled','interrupted')`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (r *notificationEmailBroadcastRepository) GetDraft(ctx context.Context, key string) ([]byte, time.Time, error) {
	if err := r.unavailable(); err != nil {
		return nil, time.Time{}, err
	}
	var payload []byte
	var saved time.Time
	err := r.db.QueryRowContext(ctx, `SELECT payload,saved_at FROM notification_email_broadcast_drafts WHERE draft_key=$1`, key).Scan(&payload, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, service.ErrSettingNotFound
	}
	return payload, saved, err
}
func (r *notificationEmailBroadcastRepository) SaveDraft(ctx context.Context, key string, payload []byte, userID int64) (time.Time, error) {
	if err := r.unavailable(); err != nil {
		return time.Time{}, err
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `INSERT INTO notification_email_broadcast_drafts(draft_key,payload,saved_by_user_id,saved_at) VALUES($1,$2,NULLIF($3,0),$4) ON CONFLICT(draft_key) DO UPDATE SET payload=EXCLUDED.payload,saved_by_user_id=EXCLUDED.saved_by_user_id,saved_at=EXCLUDED.saved_at`, key, payload, userID, now)
	return now, err
}
func (r *notificationEmailBroadcastRepository) DeleteDraft(ctx context.Context, key string) error {
	if err := r.unavailable(); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM notification_email_broadcast_drafts WHERE draft_key=$1`, key)
	return err
}
