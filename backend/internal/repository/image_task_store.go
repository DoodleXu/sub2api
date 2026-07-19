package repository

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const imageTaskKeyPrefix = "image_task:"

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
return 1
`)

type imageTaskStore struct {
	rdb        *redis.Client
	scanMu     sync.Mutex
	scanCursor uint64
}

func NewImageTaskStore(rdb *redis.Client) service.ImageTaskStore {
	return &imageTaskStore{rdb: rdb}
}

func (s *imageTaskStore) Save(ctx context.Context, task *service.ImageTaskRecord, ttl time.Duration) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, imageTaskKey(task.ID), data, ttl).Err()
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
		if len(task.PendingObjectKeys) > 0 && (task.Status == service.ImageTaskStatusProcessing || task.Status == service.ImageTaskStatusFailed) {
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
	result, err := imageTaskTransitionScript.Run(ctx, s.rdb, []string{imageTaskKey(id)}, expectedStatus, data, ttlMillis).Int64()
	if err != nil {
		return false, err
	}
	if result < 0 {
		return false, service.ErrImageTaskNotFound
	}
	return result == 1, nil
}

func imageTaskKey(id string) string {
	return imageTaskKeyPrefix + strings.TrimSpace(id)
}
