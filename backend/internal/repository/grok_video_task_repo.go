package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type grokVideoTaskRepository struct {
	db                  *sql.DB
	lastCleanupUnixNano atomic.Int64
}

const (
	grokVideoTaskRetention       = 30 * 24 * time.Hour
	grokVideoTaskCleanupInterval = time.Hour
)

func NewGrokVideoTaskRepository(db *sql.DB) service.GrokVideoTaskRepository {
	return &grokVideoTaskRepository{db: db}
}

func (r *grokVideoTaskRepository) Upsert(ctx context.Context, task *service.GrokVideoTask) error {
	if r == nil || r.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	if task == nil || strings.TrimSpace(task.RequestID) == "" || task.UserID <= 0 || task.APIKeyID <= 0 || task.AccountID <= 0 {
		return errors.New("grok video task is invalid")
	}
	r.scheduleExpiredCleanup()
	pending := task.Pending
	createdAt, err := parseGrokVideoTaskCreatedAt(pending.CreatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO grok_video_tasks (
			request_id, user_id, api_key_id, group_id, account_id,
			model, billing_model, upstream_model, original_model,
			video_resolution, video_duration_seconds, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''),
			NULLIF($10, ''), NULLIF($11, 0), $12, NOW()
		)
		ON CONFLICT (request_id, user_id, api_key_id) DO UPDATE SET
			group_id = EXCLUDED.group_id,
			account_id = EXCLUDED.account_id,
			model = EXCLUDED.model,
			billing_model = EXCLUDED.billing_model,
			upstream_model = EXCLUDED.upstream_model,
			original_model = EXCLUDED.original_model,
			video_resolution = EXCLUDED.video_resolution,
			video_duration_seconds = EXCLUDED.video_duration_seconds,
			updated_at = NOW()
	`,
		strings.TrimSpace(task.RequestID), task.UserID, task.APIKeyID, task.GroupID, task.AccountID,
		strings.TrimSpace(pending.Model), strings.TrimSpace(pending.BillingModel), strings.TrimSpace(pending.UpstreamModel), strings.TrimSpace(pending.OriginalModel),
		strings.TrimSpace(pending.VideoResolution), pending.VideoDurationSeconds, createdAt,
	)
	if err != nil {
		return fmt.Errorf("upsert grok video task: %w", err)
	}
	return nil
}

// scheduleExpiredCleanup bounds the durable task table. The task data is only needed
// while xAI accepts status/content polling; the separately durable usage log
// remains the long-term billing/audit record. Cleanup is opportunistic and
// rate-limited in a detached goroutine so it is never on the create hot path.
func (r *grokVideoTaskRepository) scheduleExpiredCleanup() {
	if r == nil || r.db == nil {
		return
	}
	now := time.Now()
	previous := r.lastCleanupUnixNano.Load()
	if previous > 0 && now.Sub(time.Unix(0, previous)) < grokVideoTaskCleanupInterval {
		return
	}
	if !r.lastCleanupUnixNano.CompareAndSwap(previous, now.UnixNano()) {
		return
	}
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = r.db.ExecContext(cleanupCtx, `
			DELETE FROM grok_video_tasks
			WHERE created_at < NOW() - ($1 * INTERVAL '1 second')
		`, int64(grokVideoTaskRetention/time.Second))
	}()
}

func (r *grokVideoTaskRepository) GetByOwner(ctx context.Context, requestID string, userID, apiKeyID int64) (*service.GrokVideoTask, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("grok video task database is unavailable")
	}
	var task service.GrokVideoTask
	var groupID sql.NullInt64
	var billingModel, upstreamModel, originalModel, resolution sql.NullString
	var duration sql.NullInt64
	var createdAt time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT request_id, user_id, api_key_id, group_id, account_id,
		       model, billing_model, upstream_model, original_model,
		       video_resolution, video_duration_seconds, created_at
		FROM grok_video_tasks
		WHERE request_id = $1 AND user_id = $2 AND api_key_id = $3
	`, strings.TrimSpace(requestID), userID, apiKeyID).Scan(
		&task.RequestID, &task.UserID, &task.APIKeyID, &groupID, &task.AccountID,
		&task.Pending.Model, &billingModel, &upstreamModel, &originalModel,
		&resolution, &duration, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGrokVideoTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get grok video task: %w", err)
	}
	if groupID.Valid {
		group := groupID.Int64
		task.GroupID = &group
	}
	task.Pending.BillingModel = billingModel.String
	task.Pending.UpstreamModel = upstreamModel.String
	task.Pending.OriginalModel = originalModel.String
	task.Pending.VideoResolution = resolution.String
	if duration.Valid {
		task.Pending.VideoDurationSeconds = int(duration.Int64)
	}
	task.Pending.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return &task, nil
}

func (r *grokVideoTaskRepository) ClaimBilling(ctx context.Context, requestID string, userID, apiKeyID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("grok video task database is unavailable")
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE grok_video_tasks
		SET billing_claimed_at = NOW(), updated_at = NOW()
		WHERE request_id = $1
		  AND user_id = $2
		  AND api_key_id = $3
		  AND billed_at IS NULL
		  AND (billing_claimed_at IS NULL OR billing_claimed_at < NOW() - INTERVAL '15 minutes')
	`, strings.TrimSpace(requestID), userID, apiKeyID)
	if err != nil {
		return false, fmt.Errorf("claim grok video task billing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read grok video task billing claim result: %w", err)
	}
	return affected == 1, nil
}

func (r *grokVideoTaskRepository) ReleaseBilling(ctx context.Context, requestID string, userID, apiKeyID int64) error {
	if r == nil || r.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE grok_video_tasks
		SET billing_claimed_at = NULL, updated_at = NOW()
		WHERE request_id = $1 AND user_id = $2 AND api_key_id = $3 AND billed_at IS NULL
	`, strings.TrimSpace(requestID), userID, apiKeyID)
	if err != nil {
		return fmt.Errorf("release grok video task billing: %w", err)
	}
	return nil
}

func (r *grokVideoTaskRepository) MarkBilled(ctx context.Context, requestID string, userID, apiKeyID int64) error {
	if r == nil || r.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE grok_video_tasks
		SET billed_at = NOW(), updated_at = NOW()
		WHERE request_id = $1 AND user_id = $2 AND api_key_id = $3 AND billed_at IS NULL
	`, strings.TrimSpace(requestID), userID, apiKeyID)
	if err != nil {
		return fmt.Errorf("mark grok video task billed: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read grok video task billed result: %w", err)
	}
	if affected == 0 {
		return service.ErrGrokVideoTaskNotFound
	}
	return nil
}

func parseGrokVideoTaskCreatedAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("parse grok video task created_at: %w", err)
	}
	return parsed.UTC(), nil
}
