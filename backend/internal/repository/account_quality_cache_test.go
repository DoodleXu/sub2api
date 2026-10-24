package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountBalanceFenceIsScopedToCredentialGeneration(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &tempUnschedCache{rdb: rdb}
	ctx := context.Background()
	require.NoError(t, cache.SetAccountBalanceBlock(ctx, 91, 0))
	blocked, err := cache.HasAccountBalanceBlock(ctx, 91, 0)
	require.NoError(t, err)
	require.True(t, blocked)
	blocked, err = cache.HasAccountBalanceBlock(ctx, 91, 1)
	require.NoError(t, err)
	require.False(t, blocked, "admin resume must not inherit the old generation's block")
	blocked, err = cache.HasAccountBalanceBlock(ctx, 92, 0)
	require.NoError(t, err)
	require.False(t, blocked)
}
