package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageGenerationArchiveRepository struct {
	db *sql.DB
}

func NewImageGenerationArchiveRepository(db *sql.DB) service.ImageGenerationArchiveRepository {
	return &imageGenerationArchiveRepository{db: db}
}

func (r *imageGenerationArchiveRepository) CreateRecord(ctx context.Context, record *service.ImageGenerationRecord) error {
	if record == nil {
		return nil
	}
	query := `
		INSERT INTO image_generation_records (
			user_id, api_key_id, group_id, account_id, request_id, source, endpoint, model,
			prompt_excerpt, image_count, status, storage_type, error_message, usage_log_id
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING id, created_at
	`
	return scanSingleRow(ctx, r.db, query, []any{
		nullableInt64(record.UserID),
		nullableInt64(record.APIKeyID),
		nullableInt64(record.GroupID),
		nullableInt64(record.AccountID),
		record.RequestID,
		record.Source,
		record.Endpoint,
		record.Model,
		record.PromptExcerpt,
		record.ImageCount,
		record.Status,
		record.StorageType,
		record.ErrorMessage,
		nullableInt64(record.UsageLogID),
	}, &record.ID, &record.CreatedAt)
}

func (r *imageGenerationArchiveRepository) UpdateRecord(ctx context.Context, record *service.ImageGenerationRecord) error {
	if record == nil || record.ID <= 0 {
		return nil
	}
	query := `
		UPDATE image_generation_records
		SET image_count = $1,
			status = $2,
			storage_type = $3,
			error_message = $4,
			completed_at = $5
		WHERE id = $6
	`
	_, err := r.db.ExecContext(ctx, query, record.ImageCount, record.Status, record.StorageType, record.ErrorMessage, nullableTime(record.CompletedAt), record.ID)
	return err
}

func (r *imageGenerationArchiveRepository) GetRecordByID(ctx context.Context, id int64) (*service.ImageGenerationRecord, []*service.ImageGenerationAsset, error) {
	record, err := r.getRecordOnly(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	assets, err := r.ListAssetsByRecordID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return record, assets, nil
}

func (r *imageGenerationArchiveRepository) getRecordOnly(ctx context.Context, id int64) (*service.ImageGenerationRecord, error) {
	query := `
		SELECT id, user_id, api_key_id, group_id, account_id, request_id, source, endpoint, model,
			prompt_excerpt, image_count, status, storage_type, error_message, usage_log_id, created_at, completed_at
		FROM image_generation_records
		WHERE id = $1
	`
	record := &service.ImageGenerationRecord{}
	var userID, apiKeyID, groupID, accountID, usageLogID sql.NullInt64
	var completedAt sql.NullTime
	err := scanSingleRow(ctx, r.db, query, []any{id},
		&record.ID, &userID, &apiKeyID, &groupID, &accountID, &record.RequestID, &record.Source, &record.Endpoint, &record.Model,
		&record.PromptExcerpt, &record.ImageCount, &record.Status, &record.StorageType, &record.ErrorMessage, &usageLogID, &record.CreatedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrImageGenerationRecordNotFound
		}
		return nil, err
	}
	record.UserID = ptrFromNullInt64(userID)
	record.APIKeyID = ptrFromNullInt64(apiKeyID)
	record.GroupID = ptrFromNullInt64(groupID)
	record.AccountID = ptrFromNullInt64(accountID)
	record.UsageLogID = ptrFromNullInt64(usageLogID)
	if completedAt.Valid {
		record.CompletedAt = &completedAt.Time
	}
	return record, nil
}

func (r *imageGenerationArchiveRepository) ListRecords(ctx context.Context, params service.ImageGenerationRecordListParams) ([]*service.ImageGenerationRecord, *service.ImageGenerationRecordListResult, error) {
	page := params.Page
	if page <= 0 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 30
	}
	if pageSize > 100 {
		pageSize = 100
	}
	where, args := imageRecordWhere(params)
	countQuery := "SELECT COUNT(*) FROM image_generation_records" + where
	var total int64
	if err := scanSingleRow(ctx, r.db, countQuery, args, &total); err != nil {
		return nil, nil, err
	}
	if total == 0 {
		return []*service.ImageGenerationRecord{}, imageRecordListResult(total, page, pageSize), nil
	}
	query := `
		SELECT id, user_id, api_key_id, group_id, account_id, request_id, source, endpoint, model,
			prompt_excerpt, image_count, status, storage_type, error_message, usage_log_id, created_at, completed_at
		FROM image_generation_records` + where + `
		ORDER BY created_at DESC, id DESC
		LIMIT $` + fmt.Sprint(len(args)+1) + ` OFFSET $` + fmt.Sprint(len(args)+2)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]*service.ImageGenerationRecord, 0, pageSize)
	for rows.Next() {
		item := &service.ImageGenerationRecord{}
		var userID, apiKeyID, groupID, accountID, usageLogID sql.NullInt64
		var completedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &userID, &apiKeyID, &groupID, &accountID, &item.RequestID, &item.Source, &item.Endpoint, &item.Model,
			&item.PromptExcerpt, &item.ImageCount, &item.Status, &item.StorageType, &item.ErrorMessage, &usageLogID, &item.CreatedAt, &completedAt,
		); err != nil {
			return nil, nil, err
		}
		item.UserID = ptrFromNullInt64(userID)
		item.APIKeyID = ptrFromNullInt64(apiKeyID)
		item.GroupID = ptrFromNullInt64(groupID)
		item.AccountID = ptrFromNullInt64(accountID)
		item.UsageLogID = ptrFromNullInt64(usageLogID)
		if completedAt.Valid {
			item.CompletedAt = &completedAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, imageRecordListResult(total, page, pageSize), nil
}

func (r *imageGenerationArchiveRepository) ListAllArchiveStorageRefs(ctx context.Context) (*service.ImageGenerationArchiveClearResult, error) {
	query := `
		WITH target_records AS (
			SELECT id FROM image_generation_records
		),
		target_assets AS (
			SELECT a.id, a.storage_key, r.storage_type
			FROM image_generation_assets a
			JOIN image_generation_records r ON r.id = a.record_id
			WHERE r.id IN (SELECT id FROM target_records)
		)
		SELECT
			(SELECT COUNT(*) FROM target_records) AS records_deleted,
			(SELECT COUNT(*) FROM target_assets) AS assets_deleted,
			COALESCE(
				(
					SELECT json_agg(
						json_build_object(
							'id', target_assets.id,
							'storage_key', target_assets.storage_key,
							'storage_type', target_assets.storage_type
						)
					)
					FROM target_assets
				),
				'[]'::json
			) AS asset_refs
	`
	var rawRefs json.RawMessage
	result := &service.ImageGenerationArchiveClearResult{}
	if err := scanSingleRow(ctx, r.db, query, nil, &result.RecordsDeleted, &result.AssetsDeleted, &rawRefs); err != nil {
		return nil, err
	}
	if len(rawRefs) > 0 {
		if err := json.Unmarshal(rawRefs, &result.AssetRefs); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *imageGenerationArchiveRepository) DeleteAllArchiveRecords(ctx context.Context) (int64, error) {
	query := `
		WITH deleted_records AS (
			DELETE FROM image_generation_records
			RETURNING id
		)
		SELECT COUNT(*) FROM deleted_records
	`
	var deleted int64
	if err := scanSingleRow(ctx, r.db, query, nil, &deleted); err != nil {
		return 0, err
	}
	return deleted, nil
}

func imageRecordWhere(params service.ImageGenerationRecordListParams) (string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if params.UserID != nil {
		add("user_id = $%d", *params.UserID)
	}
	if params.APIKeyID != nil {
		add("api_key_id = $%d", *params.APIKeyID)
	}
	if strings.TrimSpace(params.Model) != "" {
		add("model ILIKE '%' || $%d || '%'", strings.TrimSpace(params.Model))
	}
	if strings.TrimSpace(params.Status) != "" {
		add("status = $%d", strings.TrimSpace(params.Status))
	}
	if strings.TrimSpace(params.Source) != "" {
		add("source = $%d", strings.TrimSpace(params.Source))
	}
	if params.StartAt != nil {
		add("created_at >= $%d", *params.StartAt)
	}
	if params.EndAt != nil {
		add("created_at < $%d", *params.EndAt)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func imageRecordListResult(total int64, page, pageSize int) *service.ImageGenerationRecordListResult {
	pages := int(total) / pageSize
	if int(total)%pageSize > 0 {
		pages++
	}
	return &service.ImageGenerationRecordListResult{Total: total, Page: page, Size: pageSize, Pages: pages}
}

func (r *imageGenerationArchiveRepository) ListDailyStats(ctx context.Context, params service.ImageGenerationRecordDailyStatsParams) ([]service.ImageGenerationDailyStat, error) {
	query := `
		SELECT to_char(date_trunc('day', created_at), 'YYYY-MM-DD') AS day,
			COUNT(*) AS request_count,
			COALESCE(SUM(image_count), 0) AS image_count,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed_count
		FROM image_generation_records
		WHERE created_at >= $1 AND created_at < $2
		GROUP BY day
		ORDER BY day ASC
	`
	rows, err := r.db.QueryContext(ctx, query, params.StartDate, params.EndDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ImageGenerationDailyStat, 0)
	for rows.Next() {
		var item service.ImageGenerationDailyStat
		if err := rows.Scan(&item.Date, &item.RequestCount, &item.ImageCount, &item.FailedCount); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *imageGenerationArchiveRepository) GetStorageStats(ctx context.Context) (service.ImageGenerationStorageStats, error) {
	var stats service.ImageGenerationStorageStats
	query := `SELECT COALESCE(SUM(bytes), 0) FROM image_generation_assets`
	if err := scanSingleRow(ctx, r.db, query, nil, &stats.TotalBytes); err != nil {
		return service.ImageGenerationStorageStats{}, err
	}
	return stats, nil
}

func (r *imageGenerationArchiveRepository) CreateAsset(ctx context.Context, asset *service.ImageGenerationAsset) error {
	if asset == nil {
		return nil
	}
	query := `
		INSERT INTO image_generation_assets (
			record_id, asset_index, mime_type, extension, width, height, bytes, sha256,
			storage_key, public_url, admin_url
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at
	`
	return scanSingleRow(ctx, r.db, query, []any{
		asset.RecordID,
		asset.AssetIndex,
		asset.MimeType,
		asset.Extension,
		nullableInt(asset.Width),
		nullableInt(asset.Height),
		asset.Bytes,
		asset.SHA256,
		asset.StorageKey,
		asset.PublicURL,
		asset.AdminURL,
	}, &asset.ID, &asset.CreatedAt)
}

func (r *imageGenerationArchiveRepository) GetAssetByID(ctx context.Context, id int64) (*service.ImageGenerationAsset, *service.ImageGenerationRecord, error) {
	query := `
		SELECT id, record_id, asset_index, mime_type, extension, width, height, bytes, sha256,
			storage_key, public_url, admin_url, created_at
		FROM image_generation_assets
		WHERE id = $1
	`
	asset := &service.ImageGenerationAsset{}
	if err := scanAsset(ctx, r.db, query, []any{id}, asset); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, service.ErrImageGenerationRecordNotFound
		}
		return nil, nil, err
	}
	record, err := r.getRecordOnly(ctx, asset.RecordID)
	if err != nil {
		return nil, nil, err
	}
	return asset, record, nil
}

func (r *imageGenerationArchiveRepository) ListAssetsByRecordID(ctx context.Context, recordID int64) ([]*service.ImageGenerationAsset, error) {
	query := `
		SELECT id, record_id, asset_index, mime_type, extension, width, height, bytes, sha256,
			storage_key, public_url, admin_url, created_at
		FROM image_generation_assets
		WHERE record_id = $1
		ORDER BY asset_index ASC, id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, recordID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*service.ImageGenerationAsset, 0)
	for rows.Next() {
		asset := &service.ImageGenerationAsset{}
		if err := scanAssetRows(rows, asset); err != nil {
			return nil, err
		}
		out = append(out, asset)
	}
	return out, rows.Err()
}

func (r *imageGenerationArchiveRepository) CreateWebConsoleTask(ctx context.Context, task *service.WebConsoleImageTask) error {
	if task == nil {
		return nil
	}
	if len(task.RequestJSON) == 0 {
		task.RequestJSON = json.RawMessage(`{}`)
	}
	query := `
		INSERT INTO web_console_image_tasks (user_id, api_key_id, session_id, message_id, status, request_json, error_message)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at, updated_at
	`
	return scanSingleRow(ctx, r.db, query, []any{
		task.UserID,
		nullableInt64(task.APIKeyID),
		task.SessionID,
		task.MessageID,
		defaultTaskStatus(task.Status),
		task.RequestJSON,
		task.ErrorMessage,
	}, &task.ID, &task.CreatedAt, &task.UpdatedAt)
}

func (r *imageGenerationArchiveRepository) ClaimWebConsoleTask(ctx context.Context, id int64, staleBefore time.Time) (*service.WebConsoleImageTask, bool, error) {
	query := `
		UPDATE web_console_image_tasks
		SET status = 'running',
			started_at = COALESCE(started_at, NOW()),
			completed_at = NULL,
			error_message = '',
			updated_at = NOW()
		WHERE id = $1
		  AND user_deleted_at IS NULL
		  AND (
			status = 'pending'
			OR (status = 'running' AND updated_at < $2)
		  )
		RETURNING id, user_id, api_key_id, session_id, message_id, status, request_json, record_id,
			error_message, created_at, started_at, completed_at, user_deleted_at, updated_at
	`
	task := &service.WebConsoleImageTask{}
	err := scanTask(ctx, r.db, query, []any{id, staleBefore}, task)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return task, true, nil
}

func (r *imageGenerationArchiveRepository) GetWebConsoleTaskByID(ctx context.Context, id int64) (*service.WebConsoleImageTask, error) {
	query := `
		SELECT id, user_id, api_key_id, session_id, message_id, status, request_json, record_id,
			error_message, created_at, started_at, completed_at, user_deleted_at, updated_at
		FROM web_console_image_tasks
		WHERE id = $1 AND user_deleted_at IS NULL
	`
	task := &service.WebConsoleImageTask{}
	if err := scanTask(ctx, r.db, query, []any{id}, task); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrWebConsoleImageTaskNotFound
		}
		return nil, err
	}
	return task, nil
}

func (r *imageGenerationArchiveRepository) ListWebConsoleTasksByUserID(ctx context.Context, userID int64, params pagination.PaginationParams) ([]*service.WebConsoleImageTask, *pagination.PaginationResult, error) {
	var total int64
	if err := scanSingleRow(ctx, r.db, "SELECT COUNT(*) FROM web_console_image_tasks WHERE user_id = $1 AND user_deleted_at IS NULL", []any{userID}, &total); err != nil {
		return nil, nil, err
	}
	if total == 0 {
		return []*service.WebConsoleImageTask{}, paginationResultFromTotal(0, params), nil
	}
	query := `
		SELECT id, user_id, api_key_id, session_id, message_id, status, request_json, record_id,
			error_message, created_at, started_at, completed_at, user_deleted_at, updated_at
		FROM web_console_image_tasks
		WHERE user_id = $1 AND user_deleted_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.QueryContext(ctx, query, userID, params.Limit(), params.Offset())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*service.WebConsoleImageTask, 0)
	for rows.Next() {
		task := &service.WebConsoleImageTask{}
		if err := scanTaskRows(rows, task); err != nil {
			return nil, nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, paginationResultFromTotal(total, params), nil
}

func (r *imageGenerationArchiveRepository) MarkWebConsoleTasksUserDeletedBySessionID(ctx context.Context, userID int64, sessionID string) (int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if userID <= 0 || sessionID == "" {
		return 0, nil
	}
	query := `
		UPDATE web_console_image_tasks
		SET user_deleted_at = COALESCE(user_deleted_at, NOW()),
			updated_at = NOW()
		WHERE user_id = $1
		  AND session_id = $2
		  AND user_deleted_at IS NULL
	`
	result, err := r.db.ExecContext(ctx, query, userID, sessionID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *imageGenerationArchiveRepository) UpdateWebConsoleTask(ctx context.Context, task *service.WebConsoleImageTask) error {
	if task == nil || task.ID <= 0 {
		return nil
	}
	query := `
		UPDATE web_console_image_tasks
		SET status = $1,
			record_id = $2,
			error_message = $3,
			started_at = $4,
			completed_at = $5,
			updated_at = NOW()
		WHERE id = $6
	`
	_, err := r.db.ExecContext(ctx, query, defaultTaskStatus(task.Status), nullableInt64(task.RecordID), task.ErrorMessage, nullableTime(task.StartedAt), nullableTime(task.CompletedAt), task.ID)
	return err
}

func (r *imageGenerationArchiveRepository) CountDailyByDate(ctx context.Context, day time.Time) (int64, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	end := start.AddDate(0, 0, 1)
	var count int64
	err := scanSingleRow(ctx, r.db, "SELECT COUNT(*) FROM image_generation_records WHERE created_at >= $1 AND created_at < $2", []any{start, end}, &count)
	return count, err
}

func scanAsset(ctx context.Context, q sqlQueryer, query string, args []any, asset *service.ImageGenerationAsset) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return scanAssetRows(rows, asset)
}

func scanAssetRows(rows interface{ Scan(dest ...any) error }, asset *service.ImageGenerationAsset) error {
	var width, height sql.NullInt64
	if err := rows.Scan(
		&asset.ID, &asset.RecordID, &asset.AssetIndex, &asset.MimeType, &asset.Extension, &width, &height,
		&asset.Bytes, &asset.SHA256, &asset.StorageKey, &asset.PublicURL, &asset.AdminURL, &asset.CreatedAt,
	); err != nil {
		return err
	}
	if width.Valid {
		v := int(width.Int64)
		asset.Width = &v
	}
	if height.Valid {
		v := int(height.Int64)
		asset.Height = &v
	}
	return nil
}

func scanTask(ctx context.Context, q sqlQueryer, query string, args []any, task *service.WebConsoleImageTask) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return scanTaskRows(rows, task)
}

func scanTaskRows(rows interface{ Scan(dest ...any) error }, task *service.WebConsoleImageTask) error {
	var apiKeyID, recordID sql.NullInt64
	var startedAt, completedAt, userDeletedAt sql.NullTime
	if err := rows.Scan(
		&task.ID, &task.UserID, &apiKeyID, &task.SessionID, &task.MessageID, &task.Status, &task.RequestJSON, &recordID,
		&task.ErrorMessage, &task.CreatedAt, &startedAt, &completedAt, &userDeletedAt, &task.UpdatedAt,
	); err != nil {
		return err
	}
	task.APIKeyID = ptrFromNullInt64(apiKeyID)
	task.RecordID = ptrFromNullInt64(recordID)
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}
	if userDeletedAt.Valid {
		task.UserDeletedAt = &userDeletedAt.Time
	}
	return nil
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}

func ptrFromNullInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func defaultTaskStatus(v string) string {
	if strings.TrimSpace(v) == "" {
		return "pending"
	}
	return strings.TrimSpace(v)
}
