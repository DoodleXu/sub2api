package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type contentModerationRepository struct {
	db *sql.DB
}

func NewContentModerationRepository(db *sql.DB) service.ContentModerationRepository {
	return &contentModerationRepository{db: db}
}

func (r *contentModerationRepository) CreateLog(ctx context.Context, log *service.ContentModerationLog) error {
	if log == nil {
		return nil
	}
	categoryScores, err := json.Marshal(log.CategoryScores)
	if err != nil {
		return fmt.Errorf("marshal moderation category scores: %w", err)
	}
	thresholdSnapshot, err := json.Marshal(log.ThresholdSnapshot)
	if err != nil {
		return fmt.Errorf("marshal moderation thresholds: %w", err)
	}
	policySnapshot, err := json.Marshal(log.PolicySnapshot)
	if err != nil {
		return fmt.Errorf("marshal moderation policy snapshot: %w", err)
	}
	if log.PolicySnapshot == nil {
		policySnapshot = []byte("{}")
	}
	var userID any
	if log.UserID != nil {
		userID = *log.UserID
	}
	var apiKeyID any
	if log.APIKeyID != nil {
		apiKeyID = *log.APIKeyID
	}
	var groupID any
	if log.GroupID != nil {
		groupID = *log.GroupID
	}
	var policyID any
	if log.PolicyID != nil {
		policyID = *log.PolicyID
	}
	var latency any
	if log.UpstreamLatencyMS != nil {
		latency = *log.UpstreamLatencyMS
	}
	err = r.db.QueryRowContext(ctx, `
	INSERT INTO content_moderation_logs (
		    request_id, user_id, user_email, api_key_id, api_key_name, group_id, group_name,
		    endpoint, provider, model, mode, action, flagged, highest_category, highest_score,
		    category_scores, threshold_snapshot, input_excerpt, input_hash, matched_keyword, policy_id, policy_action,
		    policy_snapshot, block_status, error_code, upstream_latency_ms, error,
		    violation_count, auto_banned, email_sent, queue_delay_ms
		) VALUES (
		    $1, $2, $3, $4, $5, $6, $7,
		    $8, $9, $10, $11, $12, $13, $14, $15,
		    $16::jsonb, $17::jsonb, $18, $19, $20, $21, $22,
		    $23::jsonb, $24, $25, $26, $27,
		    $28, $29, $30, $31
		) RETURNING id, created_at`,
		log.RequestID, userID, log.UserEmail, apiKeyID, log.APIKeyName, groupID, log.GroupName,
		log.Endpoint, log.Provider, log.Model, log.Mode, log.Action, log.Flagged, log.HighestCategory, log.HighestScore,
		string(categoryScores), string(thresholdSnapshot), log.InputExcerpt, log.InputHash, log.MatchedKeyword, policyID, log.PolicyAction,
		string(policySnapshot), log.BlockStatus, log.ErrorCode, latency, log.Error,
		log.ViolationCount, log.AutoBanned, log.EmailSent, nullableIntPtr(log.QueueDelayMS),
	).Scan(&log.ID, &log.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert content moderation log: %w", err)
	}
	return nil
}

func (r *contentModerationRepository) ListLogs(ctx context.Context, filter service.ContentModerationLogFilter) ([]service.ContentModerationLog, *pagination.PaginationResult, error) {
	where, args := buildContentModerationLogWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_logs l "+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count content moderation logs: %w", err)
	}

	params := filter.Pagination
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, `
SELECT
		    l.id, l.request_id, l.user_id, l.user_email, l.api_key_id, l.api_key_name, l.group_id, l.group_name,
		    l.endpoint, l.provider, l.model, l.mode, l.action, l.flagged, l.highest_category, l.highest_score,
		    l.category_scores, l.threshold_snapshot, l.input_excerpt, l.input_hash, l.matched_keyword,
		    l.policy_id, l.policy_action, l.policy_snapshot, l.block_status, l.error_code, l.upstream_latency_ms, l.error,
		    l.violation_count, l.auto_banned, l.email_sent, COALESCE(u.status, ''), l.queue_delay_ms, l.created_at
FROM content_moderation_logs l
LEFT JOIN users u ON u.id = l.user_id `+whereSQL+`
ORDER BY l.created_at DESC, l.id DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list content moderation logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.ContentModerationLog, 0)
	for rows.Next() {
		var item service.ContentModerationLog
		var userID, apiKeyID, groupID, policyID, latency, queueDelay sql.NullInt64
		var scoresRaw, thresholdsRaw, policySnapshotRaw []byte
		if err := rows.Scan(
			&item.ID,
			&item.RequestID,
			&userID,
			&item.UserEmail,
			&apiKeyID,
			&item.APIKeyName,
			&groupID,
			&item.GroupName,
			&item.Endpoint,
			&item.Provider,
			&item.Model,
			&item.Mode,
			&item.Action,
			&item.Flagged,
			&item.HighestCategory,
			&item.HighestScore,
			&scoresRaw,
			&thresholdsRaw,
			&item.InputExcerpt,
			&item.InputHash,
			&item.MatchedKeyword,
			&policyID,
			&item.PolicyAction,
			&policySnapshotRaw,
			&item.BlockStatus,
			&item.ErrorCode,
			&latency,
			&item.Error,
			&item.ViolationCount,
			&item.AutoBanned,
			&item.EmailSent,
			&item.UserStatus,
			&queueDelay,
			&item.CreatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan content moderation log: %w", err)
		}
		if userID.Valid {
			v := userID.Int64
			item.UserID = &v
		}
		if apiKeyID.Valid {
			v := apiKeyID.Int64
			item.APIKeyID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			item.GroupID = &v
		}
		if policyID.Valid {
			v := policyID.Int64
			item.PolicyID = &v
		}
		if latency.Valid {
			v := int(latency.Int64)
			item.UpstreamLatencyMS = &v
		}
		if queueDelay.Valid {
			v := int(queueDelay.Int64)
			item.QueueDelayMS = &v
		}
		item.CategoryScores = map[string]float64{}
		_ = json.Unmarshal(scoresRaw, &item.CategoryScores)
		item.ThresholdSnapshot = map[string]float64{}
		_ = json.Unmarshal(thresholdsRaw, &item.ThresholdSnapshot)
		item.PolicySnapshot = map[string]any{}
		_ = json.Unmarshal(policySnapshotRaw, &item.PolicySnapshot)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate content moderation logs: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

func (r *contentModerationRepository) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	if userID <= 0 {
		return 0, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_logs
    WHERE user_id = $1 AND auto_banned = TRUE
)
SELECT COUNT(*)
FROM content_moderation_logs
WHERE user_id = $1
  AND flagged = TRUE
  AND action <> 'hash_block'
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, userID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user content moderation flagged logs: %w", err)
	}
	return count, nil
}

func (r *contentModerationRepository) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*service.ContentModerationCleanupResult, error) {
	result := &service.ContentModerationCleanupResult{FinishedAt: time.Now()}
	if r == nil || r.db == nil {
		return result, nil
	}
	hitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = TRUE AND created_at < $1
`, hitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired hit content moderation logs: %w", err)
	}
	result.DeletedHit, _ = hitExec.RowsAffected()

	nonHitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = FALSE AND created_at < $1
`, nonHitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired non-hit content moderation logs: %w", err)
	}
	result.DeletedNonHit, _ = nonHitExec.RowsAffected()

	result.FinishedAt = time.Now()
	return result, nil
}

func (r *contentModerationRepository) ListUserPolicies(ctx context.Context) ([]service.ContentModerationUserPolicy, error) {
	rows, err := r.db.QueryContext(ctx, `
	SELECT
	    p.id, p.user_id, COALESCE(u.email, ''), COALESCE(u.status, ''),
	    p.enabled, p.action, p.block_status, p.error_code, p.block_message,
	    p.ban_threshold, p.violation_window_hours, p.apply_to_hash_block, p.note,
	    p.created_by, p.updated_by, p.created_at, p.updated_at
	FROM content_moderation_user_policies p
	LEFT JOIN users u ON u.id = p.user_id
	ORDER BY p.updated_at DESC, p.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list content moderation user policies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.ContentModerationUserPolicy, 0)
	for rows.Next() {
		var policy service.ContentModerationUserPolicy
		var createdBy, updatedBy sql.NullInt64
		if err := rows.Scan(
			&policy.ID,
			&policy.UserID,
			&policy.UserEmail,
			&policy.UserStatus,
			&policy.Enabled,
			&policy.Action,
			&policy.BlockStatus,
			&policy.ErrorCode,
			&policy.BlockMessage,
			&policy.BanThreshold,
			&policy.ViolationWindowHours,
			&policy.ApplyToHashBlock,
			&policy.Note,
			&createdBy,
			&updatedBy,
			&policy.CreatedAt,
			&policy.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan content moderation user policy: %w", err)
		}
		if createdBy.Valid {
			v := createdBy.Int64
			policy.CreatedBy = &v
		}
		if updatedBy.Valid {
			v := updatedBy.Int64
			policy.UpdatedBy = &v
		}
		out = append(out, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate content moderation user policies: %w", err)
	}
	return out, nil
}

func (r *contentModerationRepository) CreateUserPolicy(ctx context.Context, policy *service.ContentModerationUserPolicy) error {
	if policy == nil {
		return nil
	}
	err := r.db.QueryRowContext(ctx, `
	INSERT INTO content_moderation_user_policies (
	    user_id, enabled, action, block_status, error_code, block_message,
	    ban_threshold, violation_window_hours, apply_to_hash_block, note,
	    created_by, updated_by
	) VALUES (
	    $1, $2, $3, $4, $5, $6,
	    $7, $8, $9, $10,
	    $11, $12
	)
	RETURNING id, created_at, updated_at`,
		policy.UserID, policy.Enabled, policy.Action, policy.BlockStatus, policy.ErrorCode, policy.BlockMessage,
		policy.BanThreshold, policy.ViolationWindowHours, policy.ApplyToHashBlock, policy.Note,
		nullableInt64Ptr(policy.CreatedBy), nullableInt64Ptr(policy.UpdatedBy),
	).Scan(&policy.ID, &policy.CreatedAt, &policy.UpdatedAt)
	if err != nil {
		return translatePersistenceError(err, nil, infraerrors.Conflict("CONTENT_MODERATION_POLICY_USER_EXISTS", "该用户已存在风控策略"))
	}
	return nil
}

func (r *contentModerationRepository) UpdateUserPolicy(ctx context.Context, policy *service.ContentModerationUserPolicy) error {
	if policy == nil {
		return nil
	}
	err := r.db.QueryRowContext(ctx, `
	UPDATE content_moderation_user_policies
	SET user_id = $2,
	    enabled = $3,
	    action = $4,
	    block_status = $5,
	    error_code = $6,
	    block_message = $7,
	    ban_threshold = $8,
	    violation_window_hours = $9,
	    apply_to_hash_block = $10,
	    note = $11,
	    updated_by = $12,
	    updated_at = NOW()
	WHERE id = $1
	RETURNING created_at, updated_at`,
		policy.ID, policy.UserID, policy.Enabled, policy.Action, policy.BlockStatus, policy.ErrorCode, policy.BlockMessage,
		policy.BanThreshold, policy.ViolationWindowHours, policy.ApplyToHashBlock, policy.Note, nullableInt64Ptr(policy.UpdatedBy),
	).Scan(&policy.CreatedAt, &policy.UpdatedAt)
	if err != nil {
		return translatePersistenceError(
			err,
			infraerrors.NotFound("CONTENT_MODERATION_POLICY_NOT_FOUND", "风控用户策略不存在"),
			infraerrors.Conflict("CONTENT_MODERATION_POLICY_USER_EXISTS", "该用户已存在风控策略"),
		)
	}
	return nil
}

func (r *contentModerationRepository) DeleteUserPolicy(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM content_moderation_user_policies WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete content moderation user policy: %w", err)
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return infraerrors.NotFound("CONTENT_MODERATION_POLICY_NOT_FOUND", "风控用户策略不存在")
	}
	return nil
}

func (r *contentModerationRepository) ListAllowedHashes(ctx context.Context, filter service.ContentModerationAllowedHashFilter) ([]service.ContentModerationAllowedHash, *pagination.PaginationResult, error) {
	where := []string{"h.input_hash <> ''"}
	args := make([]any, 0)
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+search+"%")
		where = append(where, fmt.Sprintf("h.input_hash ILIKE $%d", len(args)))
	}
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_allowed_hashes h "+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count content moderation allowed hashes: %w", err)
	}

	params := filter.Pagination
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, `
SELECT
    h.input_hash, h.source, h.source_log_id, COALESCE(l.input_excerpt, ''),
    h.created_by, COALESCE(u.email, ''), h.note, h.created_at
FROM content_moderation_allowed_hashes h
LEFT JOIN content_moderation_logs l ON l.id = h.source_log_id
LEFT JOIN users u ON u.id = h.created_by
`+whereSQL+`
ORDER BY h.created_at DESC, h.input_hash DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list content moderation allowed hashes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.ContentModerationAllowedHash, 0)
	for rows.Next() {
		var item service.ContentModerationAllowedHash
		var sourceLogID, createdBy sql.NullInt64
		if err := rows.Scan(
			&item.InputHash,
			&item.Source,
			&sourceLogID,
			&item.InputExcerpt,
			&createdBy,
			&item.CreatedByEmail,
			&item.Note,
			&item.CreatedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scan content moderation allowed hash: %w", err)
		}
		if sourceLogID.Valid {
			v := sourceLogID.Int64
			item.SourceLogID = &v
		}
		if createdBy.Valid {
			v := createdBy.Int64
			item.CreatedBy = &v
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate content moderation allowed hashes: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

func (r *contentModerationRepository) HasAllowedHash(ctx context.Context, inputHash string) (bool, error) {
	inputHash = strings.TrimSpace(inputHash)
	if inputHash == "" {
		return false, nil
	}
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM content_moderation_allowed_hashes WHERE input_hash = $1)`, inputHash).Scan(&exists); err != nil {
		return false, fmt.Errorf("check content moderation allowed hash: %w", err)
	}
	return exists, nil
}

func (r *contentModerationRepository) CountAllowedHashes(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_moderation_allowed_hashes`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count content moderation allowed hashes: %w", err)
	}
	return total, nil
}

func (r *contentModerationRepository) AllowHash(ctx context.Context, input service.ContentModerationAllowHashInput) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin allow content moderation hash: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
INSERT INTO content_moderation_allowed_hashes (
    input_hash, source, source_log_id, note, created_by
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (input_hash) DO NOTHING`,
		input.InputHash, input.Source, nullableInt64Ptr(input.SourceLogID), input.Note, nullableActorID(input.ActorID),
	)
	if err != nil {
		return false, fmt.Errorf("insert content moderation allowed hash: %w", err)
	}
	affected, _ := result.RowsAffected()
	added := affected > 0
	if added {
		if err := insertAllowedHashEvent(ctx, tx, service.ContentModerationAllowedHashEvent{
			Action:      "add",
			InputHash:   input.InputHash,
			ActorID:     input.ActorID,
			SourceLogID: input.SourceLogID,
			Note:        input.Note,
			Metadata: map[string]any{
				"source": input.Source,
			},
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit allow content moderation hash: %w", err)
	}
	return added, nil
}

func (r *contentModerationRepository) DeleteAllowedHash(ctx context.Context, inputHash string, actorID int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete content moderation allowed hash: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `DELETE FROM content_moderation_allowed_hashes WHERE input_hash = $1`, inputHash)
	if err != nil {
		return false, fmt.Errorf("delete content moderation allowed hash: %w", err)
	}
	affected, _ := result.RowsAffected()
	deleted := affected > 0
	if deleted {
		if err := insertAllowedHashEvent(ctx, tx, service.ContentModerationAllowedHashEvent{
			Action:    "delete",
			InputHash: inputHash,
			ActorID:   actorID,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete content moderation allowed hash: %w", err)
	}
	return deleted, nil
}

func (r *contentModerationRepository) ClearAllowedHashes(ctx context.Context, actorID int64) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin clear content moderation allowed hashes: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `DELETE FROM content_moderation_allowed_hashes`)
	if err != nil {
		return 0, fmt.Errorf("clear content moderation allowed hashes: %w", err)
	}
	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		if err := insertAllowedHashEvent(ctx, tx, service.ContentModerationAllowedHashEvent{
			Action:  "clear",
			ActorID: actorID,
			Metadata: map[string]any{
				"deleted": deleted,
			},
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit clear content moderation allowed hashes: %w", err)
	}
	return deleted, nil
}

func (r *contentModerationRepository) RecordAllowedHashEvent(ctx context.Context, event service.ContentModerationAllowedHashEvent) error {
	if err := insertAllowedHashEvent(ctx, r.db, event); err != nil {
		return err
	}
	return nil
}

type contentModerationAllowedHashEventExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAllowedHashEvent(ctx context.Context, exec contentModerationAllowedHashEventExecutor, event service.ContentModerationAllowedHashEvent) error {
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal content moderation allowed hash event metadata: %w", err)
	}
	_, err = exec.ExecContext(ctx, `
INSERT INTO content_moderation_allowed_hash_events (
    action, input_hash, actor_id, source_log_id, note, metadata
) VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		event.Action, event.InputHash, nullableActorID(event.ActorID), nullableInt64Ptr(event.SourceLogID), event.Note, string(raw),
	)
	if err != nil {
		return fmt.Errorf("insert content moderation allowed hash event: %w", err)
	}
	return nil
}

func nullableIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableActorID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func buildContentModerationLogWhere(filter service.ContentModerationLogFilter) ([]string, []any) {
	where := []string{"l.id IS NOT NULL"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	switch strings.ToLower(strings.TrimSpace(filter.Result)) {
	case "hit", "flagged":
		where = append(where, "l.flagged = TRUE")
	case "blocked", "block":
		where = append(where, "l.action IN ('block', 'keyword_block', 'hash_block')")
	case "pass", "allow":
		where = append(where, "l.flagged = FALSE AND l.error = ''")
	case "error":
		where = append(where, "l.error <> ''")
	}
	if filter.GroupID != nil {
		add("l.group_id = $%d", *filter.GroupID)
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		add("l.endpoint = $%d", endpoint)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + search + "%"
		args = append(args, like, like, like, like, like, like, like)
		idx := len(args) - 6
		where = append(where, fmt.Sprintf("(l.request_id ILIKE $%d OR l.user_email ILIKE $%d OR l.api_key_name ILIKE $%d OR l.model ILIKE $%d OR l.input_excerpt ILIKE $%d OR l.input_hash ILIKE $%d OR l.matched_keyword ILIKE $%d)", idx, idx+1, idx+2, idx+3, idx+4, idx+5, idx+6))
	}
	if filter.From != nil && !filter.From.IsZero() {
		add("l.created_at >= $%d", *filter.From)
	}
	if filter.To != nil && !filter.To.IsZero() {
		add("l.created_at <= $%d", *filter.To)
	}
	return where, args
}
