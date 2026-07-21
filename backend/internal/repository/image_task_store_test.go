package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestImageTaskStoreRoundTripAndTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)
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

func TestImageTaskStoreMissing(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)

	_, err := store.Get(context.Background(), "imgtask_missing")
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
}

func TestImageTaskStoreListsAbandonedProcessingWithoutObjectManifest(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewImageTaskStore(rdb)
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
