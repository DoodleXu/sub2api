package repository

import (
	"context"
	"encoding/json"
	"fmt"
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
	scanMu         sync.Mutex
	scanCursor     uint64
	indexMu        sync.Mutex
	indexReady     bool
	indexCleanedAt time.Time
}

func NewImageTaskStore(rdb *redis.Client) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, imageTaskKey(task.ID), data, ttl)
	for _, status := range imageTaskStatuses {
		pipe.ZRem(ctx, imageTaskStatusIndex(status), task.ID)
	}
	if isImageTaskStatus(task.Status) {
		pipe.ZAdd(ctx, imageTaskStatusIndex(task.Status), redis.Z{Score: float64(task.CreatedAt), Member: task.ID})
	}
	_, err = pipe.Exec(ctx)
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
	data, err := json.Marshal(task)
	if err != nil {
		return false, err
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
		return false, err
	}
	if result < 0 {
		return false, service.ErrImageTaskNotFound
	}
	return result == 1, nil
}

func (s *imageTaskStore) ListAdmin(ctx context.Context, query service.ImageTaskAdminQuery) (*service.ImageTaskAdminPage, error) {
	if err := s.ensureAdminIndexes(ctx); err != nil {
		return nil, err
	}
	if err := s.cleanupAdminIndexes(ctx); err != nil {
		return nil, err
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
		statusTasks, err := s.listAdminStatusTasks(ctx, status, query.Limit, hasCursor, cursorCreatedAt, cursorID)
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
		count, countErr := s.rdb.ZCard(ctx, imageTaskStatusIndex(status)).Result()
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

func (s *imageTaskStore) listAdminStatusTasks(ctx context.Context, status string, limit int, hasCursor bool, cursorCreatedAt int64, cursorID string) ([]*service.ImageTaskRecord, error) {
	const batchSize int64 = 256
	maxScore := "+inf"
	if hasCursor {
		maxScore = strconv.FormatInt(cursorCreatedAt, 10)
	}
	tasks := make([]*service.ImageTaskRecord, 0, limit+1)
	var offset int64
	for {
		entries, err := s.rdb.ZRevRangeByScoreWithScores(ctx, imageTaskStatusIndex(status), &redis.ZRangeBy{
			Max: maxScore, Min: "-inf", Offset: offset, Count: batchSize,
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
				stale := make([]interface{}, 0)
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
