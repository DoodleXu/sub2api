package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const imageTaskKeyPrefix = "image_task:"
const imageTaskIndexPrefix = "image_task:index:"
const imageTaskIndexCleanupInterval = 10 * time.Second
const imageTaskHistoryReconcileTimeout = 5 * time.Second

var imageTaskStatuses = []string{
	service.ImageTaskStatusProcessing,
	service.ImageTaskStatusCompleted,
	service.ImageTaskStatusFailed,
}

var imageTaskTransitionScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then
  return -1
end
local current = cjson.decode(raw)
if current.status ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('ZREM', KEYS[2], ARGV[5])
redis.call('ZADD', KEYS[3], ARGV[4], ARGV[5])
return 1
`)

type imageTaskStore struct {
	rdb            *redis.Client
	db             *sql.DB
	scanMu         sync.Mutex
	scanCursor     uint64
	indexMu        sync.Mutex
	indexReady     bool
	indexCleanedAt time.Time
}

func NewImageTaskStore(rdb *redis.Client, db *sql.DB) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb, db: db}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	var historyTx *sql.Tx
	if s.db != nil {
		historyTx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func(tx *sql.Tx) { _ = tx.Rollback() }(historyTx)
		if err := upsertImageTaskHistory(ctx, historyTx, task); err != nil {
			return err
		}
	}
	if err = s.saveRuntime(ctx, task, data, ttl); err != nil {
		return err
	}
	if historyTx != nil {
		if err = historyTx.Commit(); err != nil {
			if cleanupErr := s.deleteRuntimeDetached(task.ID); cleanupErr != nil {
				return fmt.Errorf("commit image task history: %w; rollback runtime: %v", err, cleanupErr)
			}
			return err
		}
	}
	return nil
}

func (s *imageTaskStore) saveRuntime(ctx context.Context, task *service.ImageTaskRecord, data []byte, ttl time.Duration) error {
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, imageTaskKey(task.ID), data, ttl)
	for _, status := range imageTaskStatuses {
		pipe.ZRem(ctx, imageTaskStatusIndex(status), task.ID)
	}
	if isImageTaskStatus(task.Status) {
		pipe.ZAdd(ctx, imageTaskStatusIndex(task.Status), redis.Z{Score: float64(task.CreatedAt), Member: task.ID})
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *imageTaskStore) Get(ctx context.Context, id string) (*service.ImageTaskRecord, error) {
	data, err := s.rdb.Get(ctx, imageTaskKey(id)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTaskRecord
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// ListPending scans a bounded batch of durable task manifests. The service
// worker uses this after restarts to retry failed cleanup and expire abandoned
// processing tasks without requiring a client poll.
func (s *imageTaskStore) ListPending(ctx context.Context, limit int) ([]*service.ImageTaskRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	result := make([]*service.ImageTaskRecord, 0, limit)
	s.scanMu.Lock()
	cursor := s.scanCursor
	s.scanMu.Unlock()
	batch, next, scanErr := s.rdb.Scan(ctx, cursor, imageTaskKeyPrefix+"*", 128).Result()
	if scanErr != nil {
		return nil, scanErr
	}
	s.scanMu.Lock()
	s.scanCursor = next
	s.scanMu.Unlock()
	for _, key := range batch {
		if strings.HasPrefix(key, imageTaskIndexPrefix) {
			continue
		}
		data, getErr := s.rdb.Get(ctx, key).Bytes()
		if getErr != nil {
			if getErr == redis.Nil {
				continue
			}
			return nil, getErr
		}
		var task service.ImageTaskRecord
		if unmarshalErr := json.Unmarshal(data, &task); unmarshalErr != nil {
			// A malformed/legacy key must not starve valid cleanup records.
			continue
		}
		if task.Status == service.ImageTaskStatusProcessing ||
			(task.Status == service.ImageTaskStatusFailed && len(task.PendingObjectKeys) > 0) {
			result = append(result, &task)
			if len(result) >= limit {
				return result, nil
			}
		}
	}
	return result, nil
}

func (s *imageTaskStore) Transition(ctx context.Context, id, expectedStatus string, task *service.ImageTaskRecord, ttl time.Duration) (bool, error) {
	previous, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if previous.Status != expectedStatus {
		return false, nil
	}
	data, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	var historyTx *sql.Tx
	if s.db != nil {
		historyTx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer func(tx *sql.Tx) { _ = tx.Rollback() }(historyTx)
		if err := upsertImageTaskHistory(ctx, historyTx, task); err != nil {
			return false, err
		}
	}
	ttlMillis := ttl.Milliseconds()
	if ttlMillis <= 0 {
		ttlMillis = 1
	}
	result, err := imageTaskTransitionScript.Run(ctx, s.rdb, []string{
		imageTaskKey(id),
		imageTaskStatusIndex(expectedStatus),
		imageTaskStatusIndex(task.Status),
	}, expectedStatus, data, ttlMillis, task.CreatedAt, task.ID).Int64()
	if err != nil {
		if historyTx != nil {
			_ = historyTx.Rollback()
		}
		s.reconcileHistoryFromRuntimeDetached(id)
		return false, err
	}
	if result < 0 {
		if historyTx != nil {
			_ = historyTx.Rollback()
		}
		if historyErr := s.markExpiredHistoryDetached(previous); historyErr != nil {
			return false, fmt.Errorf("%w: persist expired image task history: %v", service.ErrImageTaskNotFound, historyErr)
		}
		return false, service.ErrImageTaskNotFound
	}
	if result != 1 {
		if historyTx != nil {
			_ = historyTx.Rollback()
		}
		s.reconcileHistoryFromRuntimeDetached(id)
		return false, nil
	}
	if historyTx != nil {
		if err := historyTx.Commit(); err != nil {
			s.reconcileHistoryFromRuntimeDetached(id)
			return false, err
		}
	}
	return true, nil
}

func (s *imageTaskStore) ListAdmin(ctx context.Context, query service.ImageTaskAdminQuery) (*service.ImageTaskAdminPage, error) {
	if err := s.ensureAdminIndexes(ctx); err != nil {
		return nil, err
	}
	if err := s.cleanupAdminIndexes(ctx); err != nil {
		return nil, err
	}
	if s.db != nil {
		return s.listAdminHistory(ctx, query)
	}
	statuses := imageTaskStatuses
	if query.Status != "" && query.Status != "all" {
		statuses = []string{query.Status}
	}
	cursorCreatedAt, cursorID, err := service.DecodeImageTaskAdminCursor(query.Cursor)
	if err != nil {
		return nil, err
	}
	hasCursor := cursorID != ""
	seen := make(map[string]struct{})
	tasks := make([]*service.ImageTaskRecord, 0, query.Limit+1)
	for _, status := range statuses {
		statusTasks, err := s.listAdminStatusTasks(ctx, status, query.Limit, hasCursor, cursorCreatedAt, cursorID, query.StartAt, query.EndAt)
		if err != nil {
			return nil, err
		}
		for _, task := range statusTasks {
			if _, ok := seen[task.ID]; ok {
				continue
			}
			seen[task.ID] = struct{}{}
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt == tasks[j].CreatedAt {
			return tasks[i].ID > tasks[j].ID
		}
		return tasks[i].CreatedAt > tasks[j].CreatedAt
	})
	hasMore := len(tasks) > query.Limit
	if hasMore {
		tasks = tasks[:query.Limit]
	}
	page := &service.ImageTaskAdminPage{Tasks: tasks, HasMore: hasMore}
	if len(tasks) > 0 && hasMore {
		last := tasks[len(tasks)-1]
		page.NextCursor = service.EncodeImageTaskAdminCursor(last.CreatedAt, last.ID)
	}
	for _, status := range imageTaskStatuses {
		count, countErr := s.countAdminStatus(ctx, status, query.StartAt, query.EndAt)
		if countErr != nil {
			return nil, countErr
		}
		switch status {
		case service.ImageTaskStatusProcessing:
			page.Stats.Processing = int(count)
		case service.ImageTaskStatusCompleted:
			page.Stats.Completed = int(count)
		case service.ImageTaskStatusFailed:
			page.Stats.Failed = int(count)
		}
	}
	return page, nil
}

func (s *imageTaskStore) listAdminStatusTasks(ctx context.Context, status string, limit int, hasCursor bool, cursorCreatedAt int64, cursorID string, startAt, endAt int64) ([]*service.ImageTaskRecord, error) {
	const batchSize int64 = 256
	maxScore := "+inf"
	minScore := "-inf"
	if endAt > 0 {
		maxScore = strconv.FormatInt(endAt-1, 10)
	}
	if startAt > 0 {
		minScore = strconv.FormatInt(startAt, 10)
	}
	if hasCursor {
		cursorMax := strconv.FormatInt(cursorCreatedAt, 10)
		if endAt <= 0 || cursorCreatedAt < endAt {
			maxScore = cursorMax
		}
	}
	tasks := make([]*service.ImageTaskRecord, 0, limit+1)
	var offset int64
	for {
		entries, err := s.rdb.ZRevRangeByScoreWithScores(ctx, imageTaskStatusIndex(status), &redis.ZRangeBy{
			Max: maxScore, Min: minScore, Offset: offset, Count: batchSize,
		}).Result()
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			break
		}
		offset += int64(len(entries))
		for _, entry := range entries {
			id := fmt.Sprint(entry.Member)
			task, getErr := s.Get(ctx, id)
			if getErr != nil {
				if getErr == service.ErrImageTaskNotFound {
					continue
				}
				return nil, getErr
			}
			if task.Status != status || (hasCursor && (task.CreatedAt > cursorCreatedAt || (task.CreatedAt == cursorCreatedAt && task.ID >= cursorID))) {
				continue
			}
			tasks = append(tasks, task)
			if len(tasks) > limit {
				return tasks, nil
			}
		}
		if int64(len(entries)) < batchSize {
			break
		}
	}
	return tasks, nil
}

func (s *imageTaskStore) countAdminStatus(ctx context.Context, status string, startAt, endAt int64) (int64, error) {
	if startAt <= 0 && endAt <= 0 {
		return s.rdb.ZCard(ctx, imageTaskStatusIndex(status)).Result()
	}
	min, max := "-inf", "+inf"
	if startAt > 0 {
		min = strconv.FormatInt(startAt, 10)
	}
	if endAt > 0 {
		max = strconv.FormatInt(endAt-1, 10)
	}
	return s.rdb.ZCount(ctx, imageTaskStatusIndex(status), min, max).Result()
}

func (s *imageTaskStore) upsertHistory(ctx context.Context, task *service.ImageTaskRecord) error {
	if s.db == nil || task == nil {
		return nil
	}
	return upsertImageTaskHistory(ctx, s.db, task)
}

func upsertImageTaskHistory(ctx context.Context, executor sqlExecutor, task *service.ImageTaskRecord) error {
	if executor == nil || task == nil {
		return nil
	}
	var resultJSON, errorJSON any
	if len(task.Result) > 0 {
		resultJSON = string(task.Result)
	}
	if len(task.Error) > 0 {
		errorJSON = string(task.Error)
	}
	_, err := executor.ExecContext(ctx, `
INSERT INTO image_task_history (
    task_id, user_id, api_key_id, platform, operation, model, image_count,
    status, http_status, result_json, error_json, created_at, completed_at, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
    to_timestamp($12), CASE WHEN $13::bigint > 0 THEN to_timestamp($13) ELSE NULL END, to_timestamp($14)
)
ON CONFLICT (task_id) DO UPDATE SET
    status = EXCLUDED.status,
    http_status = EXCLUDED.http_status,
    result_json = EXCLUDED.result_json,
    error_json = EXCLUDED.error_json,
    completed_at = EXCLUDED.completed_at,
    expires_at = EXCLUDED.expires_at`,
		task.ID, task.UserID, task.APIKeyID, task.Platform, task.Operation, task.Model, task.ImageCount,
		task.Status, task.HTTPStatus, resultJSON, errorJSON, task.CreatedAt, unixTimeValue(task.CompletedAt), task.ExpiresAt)
	return err
}

func (s *imageTaskStore) reconcileHistoryFromRuntimeDetached(id string) {
	if s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageTaskHistoryReconcileTimeout)
	defer cancel()
	current, err := s.Get(ctx, id)
	if err != nil {
		return
	}
	_ = s.upsertHistory(ctx, current)
}

func (s *imageTaskStore) markExpiredHistoryDetached(previous *service.ImageTaskRecord) error {
	if s.db == nil || previous == nil {
		return nil
	}
	expired := *previous
	completedAt := time.Now().UTC().Unix()
	expired.Status = service.ImageTaskStatusFailed
	expired.HTTPStatus = http.StatusGone
	expired.Result = nil
	expired.Error = json.RawMessage(`{"error":{"type":"task_expired","message":"image task runtime state expired before transition completed"}}`)
	expired.CompletedAt = &completedAt
	ctx, cancel := context.WithTimeout(context.Background(), imageTaskHistoryReconcileTimeout)
	defer cancel()
	return s.upsertHistory(ctx, &expired)
}

func (s *imageTaskStore) deleteRuntimeDetached(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), imageTaskHistoryReconcileTimeout)
	defer cancel()
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, imageTaskKey(id))
	for _, status := range imageTaskStatuses {
		pipe.ZRem(ctx, imageTaskStatusIndex(status), id)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *imageTaskStore) listAdminHistory(ctx context.Context, query service.ImageTaskAdminQuery) (*service.ImageTaskAdminPage, error) {
	cursorCreatedAt, cursorID, err := service.DecodeImageTaskAdminCursor(query.Cursor)
	if err != nil {
		return nil, err
	}
	where := []string{"1=1"}
	args := make([]any, 0, 8)
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	if query.Status != "" && query.Status != "all" {
		where = append(where, "status = "+addArg(query.Status))
	}
	if query.StartAt > 0 {
		where = append(where, "created_at >= to_timestamp("+addArg(query.StartAt)+")")
	}
	if query.EndAt > 0 {
		where = append(where, "created_at < to_timestamp("+addArg(query.EndAt)+")")
	}
	if cursorID != "" {
		createdArg := addArg(cursorCreatedAt)
		idArg := addArg(cursorID)
		where = append(where, "(created_at < to_timestamp("+createdArg+") OR (created_at = to_timestamp("+createdArg+") AND task_id < "+idArg+"))")
	}
	limitArg := addArg(query.Limit + 1)
	rows, err := s.db.QueryContext(ctx, `
SELECT task_id, user_id, api_key_id, platform, operation, model, image_count,
       status, http_status, result_json, error_json,
       EXTRACT(EPOCH FROM created_at)::bigint,
       CASE WHEN completed_at IS NULL THEN NULL ELSE EXTRACT(EPOCH FROM completed_at)::bigint END,
       EXTRACT(EPOCH FROM expires_at)::bigint
FROM image_task_history
WHERE `+strings.Join(where, " AND ")+`
ORDER BY created_at DESC, task_id DESC
LIMIT `+limitArg, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]*service.ImageTaskRecord, 0, query.Limit+1)
	for rows.Next() {
		var task service.ImageTaskRecord
		var completedAt sql.NullInt64
		var resultJSON, errorJSON []byte
		if err := rows.Scan(&task.ID, &task.UserID, &task.APIKeyID, &task.Platform, &task.Operation, &task.Model, &task.ImageCount,
			&task.Status, &task.HTTPStatus, &resultJSON, &errorJSON, &task.CreatedAt, &completedAt, &task.ExpiresAt); err != nil {
			return nil, err
		}
		task.Result = resultJSON
		task.Error = errorJSON
		if completedAt.Valid {
			value := completedAt.Int64
			task.CompletedAt = &value
		}
		tasks = append(tasks, &task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	page := &service.ImageTaskAdminPage{HasMore: len(tasks) > query.Limit}
	if page.HasMore {
		tasks = tasks[:query.Limit]
	}
	page.Tasks = tasks
	if page.HasMore && len(tasks) > 0 {
		last := tasks[len(tasks)-1]
		page.NextCursor = service.EncodeImageTaskAdminCursor(last.CreatedAt, last.ID)
	}
	statsArgs := make([]any, 0, 2)
	statsWhere := []string{"1=1"}
	if query.StartAt > 0 {
		statsArgs = append(statsArgs, query.StartAt)
		statsWhere = append(statsWhere, "created_at >= to_timestamp($"+strconv.Itoa(len(statsArgs))+")")
	}
	if query.EndAt > 0 {
		statsArgs = append(statsArgs, query.EndAt)
		statsWhere = append(statsWhere, "created_at < to_timestamp($"+strconv.Itoa(len(statsArgs))+")")
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE status = 'processing'),
       COUNT(*) FILTER (WHERE status = 'completed'),
       COUNT(*) FILTER (WHERE status = 'failed')
FROM image_task_history WHERE `+strings.Join(statsWhere, " AND "), statsArgs...).Scan(
		&page.Stats.Processing, &page.Stats.Completed, &page.Stats.Failed)
	if err != nil {
		return nil, err
	}
	return page, nil
}

func unixTimeValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *imageTaskStore) cleanupAdminIndexes(ctx context.Context) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	now := time.Now()
	if !s.indexCleanedAt.IsZero() && now.Sub(s.indexCleanedAt) < imageTaskIndexCleanupInterval {
		return nil
	}
	for _, status := range imageTaskStatuses {
		var cursor uint64
		for {
			members, next, err := s.rdb.ZScan(ctx, imageTaskStatusIndex(status), cursor, "*", 256).Result()
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(members)/2)
			ids := make([]string, 0, len(members)/2)
			for index := 0; index+1 < len(members); index += 2 {
				ids = append(ids, members[index])
				keys = append(keys, imageTaskKey(members[index]))
			}
			if len(keys) > 0 {
				values, err := s.rdb.MGet(ctx, keys...).Result()
				if err != nil {
					return err
				}
				stale := make([]any, 0)
				for index, value := range values {
					if value == nil {
						stale = append(stale, ids[index])
					}
				}
				if len(stale) > 0 {
					if err := s.rdb.ZRem(ctx, imageTaskStatusIndex(status), stale...).Err(); err != nil {
						return err
					}
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	s.indexCleanedAt = now
	return nil
}

func (s *imageTaskStore) ensureAdminIndexes(ctx context.Context) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexReady {
		return nil
	}
	var cursor uint64
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, imageTaskKeyPrefix+"*", 256).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			if strings.HasPrefix(key, imageTaskIndexPrefix) {
				continue
			}
			data, err := s.rdb.Get(ctx, key).Bytes()
			if err != nil {
				if err == redis.Nil {
					continue
				}
				return err
			}
			var task service.ImageTaskRecord
			if err := json.Unmarshal(data, &task); err != nil || task.ID == "" || !isImageTaskStatus(task.Status) {
				continue
			}
			if err := s.rdb.ZAdd(ctx, imageTaskStatusIndex(task.Status), redis.Z{Score: float64(task.CreatedAt), Member: task.ID}).Err(); err != nil {
				return err
			}
			if err := s.upsertHistory(ctx, &task); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	s.indexReady = true
	return nil
}

func imageTaskStatusIndex(status string) string {
	return imageTaskIndexPrefix + status
}

func isImageTaskStatus(status string) bool {
	return status == service.ImageTaskStatusProcessing || status == service.ImageTaskStatusCompleted || status == service.ImageTaskStatusFailed
}

func imageTaskKey(id string) string {
	return imageTaskKeyPrefix + strings.TrimSpace(id)
}
