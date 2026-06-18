package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestContentModerationHashCacheNotificationDedupeReturnsLastSentAt(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewContentModerationHashCache(rdb)

	acquired, lastSentAt, err := cache.TryAcquireNotificationDedupe(context.Background(), "cm:email:test", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Nil(t, lastSentAt)

	acquired, lastSentAt, err = cache.TryAcquireNotificationDedupe(context.Background(), "cm:email:test", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NotNil(t, lastSentAt)
}

func TestContentModerationHashCacheNotificationDedupeKeepsLegacyValueDeduped(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewContentModerationHashCache(rdb)

	require.NoError(t, rdb.Set(context.Background(), "cm:email:legacy", "1", time.Minute).Err())

	acquired, lastSentAt, err := cache.TryAcquireNotificationDedupe(context.Background(), "cm:email:legacy", time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, lastSentAt)
}
