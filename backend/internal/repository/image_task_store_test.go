package repository

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type deleteImageTaskBeforeEvalHook struct {
	once    sync.Once
	control *redis.Client
	key     string
}

func (h *deleteImageTaskBeforeEvalHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *deleteImageTaskBeforeEvalHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "eval" || cmd.Name() == "evalsha" {
			h.once.Do(func() { _ = h.control.Del(context.Background(), h.key).Err() })
		}
		return next(ctx, cmd)
	}
}

func (h *deleteImageTaskBeforeEvalHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestImageTaskStoreListsPersistentHistoryWithDateRange(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewImageTaskStore(rdb, db)

	columns := []string{"task_id", "user_id", "api_key_id", "platform", "operation", "model", "image_count", "status", "http_status", "result_json", "error_json", "created_at", "completed_at", "expires_at"}
	mock.ExpectQuery(`SELECT task_id, user_id, api_key_id, platform, operation, model, image_count,`).
		WithArgs(service.ImageTaskStatusCompleted, int64(100), int64(200), 11).
		WillReturnRows(sqlmock.NewRows(columns).AddRow("imgtask_history", int64(7), int64(9), "openai", "generation", "gpt-image-1", 1, "completed", 200, []byte(`{"data":[]}`), nil, int64(150), int64(160), int64(300)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FILTER`).
		WithArgs(int64(100), int64(200)).
		WillReturnRows(sqlmock.NewRows([]string{"processing", "completed", "failed"}).AddRow(0, 1, 0))

	page, err := store.(service.ImageTaskAdminStore).ListAdmin(context.Background(), service.ImageTaskAdminQuery{
		Status: service.ImageTaskStatusCompleted, StartAt: 100, EndAt: 200, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 1)
	require.Equal(t, "imgtask_history", page.Tasks[0].ID)
	require.Equal(t, 1, page.Stats.Completed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageTaskStoreRoundTripAndTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	task := &service.ImageTaskRecord{
		ID:        "imgtask_123",
		UserID:    7,
		APIKeyID:  9,
		Status:    service.ImageTaskStatusProcessing,
		CreatedAt: 100,
		ExpiresAt: 200,
	}

	require.NoError(t, store.Save(context.Background(), task, 24*time.Hour))
	got, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, task, got)
	require.Equal(t, 24*time.Hour, mr.TTL(imageTaskKey(task.ID)))

	completed := *task
	completed.Status = service.ImageTaskStatusCompleted
	transitioned, err := store.Transition(context.Background(), task.ID, service.ImageTaskStatusProcessing, &completed, time.Hour)
	require.NoError(t, err)
	require.True(t, transitioned)
	transitioned, err = store.Transition(context.Background(), task.ID, service.ImageTaskStatusProcessing, task, time.Hour)
	require.NoError(t, err)
	require.False(t, transitioned, "terminal state must not be overwritten")
	got, err = store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
}

func TestImageTaskStoreSaveRollsBackHistoryWhenRuntimeWriteFails(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewImageTaskStore(rdb, db)
	task := &service.ImageTaskRecord{
		ID: "imgtask_runtime_failure", UserID: 7, APIKeyID: 9,
		Status: service.ImageTaskStatusProcessing, CreatedAt: 100, ExpiresAt: 200,
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO image_task_history").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	mr.Close()

	err = store.Save(context.Background(), task, time.Hour)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageTaskStoreTransitionMarksHistoryFailedWhenRuntimeExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	control := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		_ = control.Close()
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewImageTaskStore(rdb, db)
	task := &service.ImageTaskRecord{
		ID: "imgtask_expired_during_transition", UserID: 7, APIKeyID: 9,
		Status: service.ImageTaskStatusProcessing, CreatedAt: 100, ExpiresAt: 200,
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO image_task_history").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, store.Save(context.Background(), task, time.Hour))

	rdb.AddHook(&deleteImageTaskBeforeEvalHook{control: control, key: imageTaskKey(task.ID)})
	completed := *task
	completed.Status = service.ImageTaskStatusCompleted
	completedAt := int64(150)
	completed.CompletedAt = &completedAt

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO image_task_history").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectRollback()
	mock.ExpectExec("INSERT INTO image_task_history").
		WithArgs(
			task.ID, task.UserID, task.APIKeyID, task.Platform, task.Operation, task.Model, task.ImageCount,
			service.ImageTaskStatusFailed, 410, nil, sqlmock.AnyArg(), task.CreatedAt, sqlmock.AnyArg(), task.ExpiresAt,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	transitioned, err := store.Transition(context.Background(), task.ID, service.ImageTaskStatusProcessing, &completed, time.Hour)
	require.False(t, transitioned)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImageTaskStoreMissing(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)

	_, err := store.Get(context.Background(), "imgtask_missing")
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

func TestImageTaskStoreListsAbandonedProcessingWithoutObjectManifest(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	ctx := context.Background()
	tasks := []*service.ImageTaskRecord{
		{ID: "imgtask_processing", Status: service.ImageTaskStatusProcessing},
		{ID: "imgtask_failed_pending", Status: service.ImageTaskStatusFailed, PendingObjectKeys: []string{"images/one.png"}},
		{ID: "imgtask_failed_clean", Status: service.ImageTaskStatusFailed},
		{ID: "imgtask_completed", Status: service.ImageTaskStatusCompleted, PendingObjectKeys: []string{"images/done.png"}},
	}
	for _, task := range tasks {
		require.NoError(t, store.Save(ctx, task, time.Hour))
	}

	pending, err := store.ListPending(ctx, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(pending))
	for _, task := range pending {
		ids = append(ids, task.ID)
	}
	require.ElementsMatch(t, []string{"imgtask_processing", "imgtask_failed_pending"}, ids)
}

func TestImageTaskStoreListsAdminTasksByStatusAndCursor(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	ctx := context.Background()
	for _, task := range []*service.ImageTaskRecord{
		{ID: "imgtask_old", Status: service.ImageTaskStatusCompleted, CreatedAt: 10},
		{ID: "imgtask_new", Status: service.ImageTaskStatusProcessing, CreatedAt: 30},
		{ID: "imgtask_failed", Status: service.ImageTaskStatusFailed, CreatedAt: 20},
	} {
		require.NoError(t, store.Save(ctx, task, time.Hour))
	}
	adminStore, ok := store.(service.ImageTaskAdminStore)
	require.True(t, ok)

	first, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: "all", Limit: 2})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.Equal(t, []string{"imgtask_new", "imgtask_failed"}, []string{first.Tasks[0].ID, first.Tasks[1].ID})
	require.Equal(t, 1, first.Stats.Processing)
	require.Equal(t, 1, first.Stats.Completed)
	require.Equal(t, 1, first.Stats.Failed)

	second, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: "all", Cursor: first.NextCursor, Limit: 2})
	require.NoError(t, err)
	require.False(t, second.HasMore)
	require.Equal(t, []string{"imgtask_old"}, []string{second.Tasks[0].ID})

	failed, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: service.ImageTaskStatusFailed, Limit: 10})
	require.NoError(t, err)
	require.Len(t, failed.Tasks, 1)
	require.Equal(t, "imgtask_failed", failed.Tasks[0].ID)
}

func TestImageTaskStoreCleansExpiredAdminIndexMembers(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	ctx := context.Background()
	valid := &service.ImageTaskRecord{ID: "imgtask_valid", Status: service.ImageTaskStatusCompleted, CreatedAt: 20}
	expired := &service.ImageTaskRecord{ID: "imgtask_expired", Status: service.ImageTaskStatusFailed, CreatedAt: 10}
	require.NoError(t, store.Save(ctx, valid, time.Hour))
	require.NoError(t, store.Save(ctx, expired, time.Second))
	mr.FastForward(2 * time.Second)

	adminStore, ok := store.(service.ImageTaskAdminStore)
	require.True(t, ok)
	page, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: "all", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 0, page.Stats.Failed)
	require.Equal(t, 1, page.Stats.Completed)
	require.Equal(t, []string{"imgtask_valid"}, []string{page.Tasks[0].ID})
	_, err = rdb.ZScore(ctx, imageTaskStatusIndex(service.ImageTaskStatusFailed), expired.ID).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestImageTaskStoreAdminCursorHandlesLegacyZeroCreatedAt(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	ctx := context.Background()
	for _, task := range []*service.ImageTaskRecord{
		{ID: "imgtask_zero_b", Status: service.ImageTaskStatusCompleted, CreatedAt: 0},
		{ID: "imgtask_zero_a", Status: service.ImageTaskStatusCompleted, CreatedAt: 0},
	} {
		require.NoError(t, store.Save(ctx, task, time.Hour))
	}

	adminStore, ok := store.(service.ImageTaskAdminStore)
	require.True(t, ok)
	first, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: service.ImageTaskStatusCompleted, Limit: 1})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.Equal(t, "imgtask_zero_b", first.Tasks[0].ID)

	second, err := adminStore.ListAdmin(ctx, service.ImageTaskAdminQuery{Status: service.ImageTaskStatusCompleted, Cursor: first.NextCursor, Limit: 1})
	require.NoError(t, err)
	require.False(t, second.HasMore)
	require.Equal(t, "imgtask_zero_a", second.Tasks[0].ID)
}

func TestImageTaskStoreAdminCursorDoesNotDropLargeSameSecondPage(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb, nil)
	ctx := context.Background()
	const total = 2050
	for index := 0; index < total; index++ {
		taskID := fmt.Sprintf("imgtask_same_second_%04d", index)
		require.NoError(t, store.Save(ctx, &service.ImageTaskRecord{
			ID: taskID, Status: service.ImageTaskStatusCompleted, CreatedAt: 42,
		}, time.Hour))
	}

	adminStore, ok := store.(service.ImageTaskAdminStore)
	require.True(t, ok)
	seen := make(map[string]struct{}, total)
	query := service.ImageTaskAdminQuery{Status: service.ImageTaskStatusCompleted, Limit: 50}
	for {
		page, err := adminStore.ListAdmin(ctx, query)
		require.NoError(t, err)
		for _, task := range page.Tasks {
			seen[task.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		require.NotEmpty(t, page.NextCursor)
		query.Cursor = page.NextCursor
	}
	require.Len(t, seen, total)
}
