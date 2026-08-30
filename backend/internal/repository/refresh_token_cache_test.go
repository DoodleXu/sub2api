package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newRefreshTokenCacheTest(t *testing.T) (*redis.Client, service.RefreshTokenCache) {
	t.Helper()
	server := miniredis.NewMiniRedis()
	if err := server.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, NewRefreshTokenCache(client)
}

func TestRefreshTokenCacheConsumeIsAtomicAndKeepsReuseMetadata(t *testing.T) {
	client, cache := newRefreshTokenCacheTest(t)
	consumer, ok := cache.(service.AtomicRefreshTokenConsumer)
	require.True(t, ok, "production Redis cache must expose atomic consume")
	ctx := context.Background()
	data := &service.RefreshTokenData{
		UserID:    7,
		FamilyID:  "family-atomic",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	require.NoError(t, cache.StoreRefreshToken(ctx, "hash-1", data, 10*time.Minute))

	first, reused, err := consumer.ConsumeRefreshToken(ctx, "hash-1")
	require.NoError(t, err)
	require.False(t, reused)
	require.Equal(t, data.FamilyID, first.FamilyID)
	_, err = cache.GetRefreshToken(ctx, "hash-1")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)

	second, reused, err := consumer.ConsumeRefreshToken(ctx, "hash-1")
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, data.FamilyID, second.FamilyID)
	usedTTL, err := client.PTTL(ctx, refreshTokenUsedKey("hash-1")).Result()
	require.NoError(t, err)
	require.Greater(t, usedTTL, time.Duration(0))
}

func TestRefreshTokenCacheConcurrentConsumeHasOneWinner(t *testing.T) {
	_, cache := newRefreshTokenCacheTest(t)
	consumer, ok := cache.(service.AtomicRefreshTokenConsumer)
	require.True(t, ok)
	ctx := context.Background()
	require.NoError(t, cache.StoreRefreshToken(ctx, "hash-race", &service.RefreshTokenData{
		UserID: 1, FamilyID: "family-race", ExpiresAt: time.Now().UTC().Add(time.Minute),
	}, time.Minute))

	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, replays := 0, 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reused, err := consumer.ConsumeRefreshToken(ctx, "hash-race")
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			if reused {
				replays++
			} else {
				winners++
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, winners)
	require.Equal(t, callers-1, replays)
}

func TestRefreshTokenCacheFamilyRevokeBlocksLateRotation(t *testing.T) {
	client, cache := newRefreshTokenCacheTest(t)
	ctx := context.Background()
	familyID := "family-revoked"
	require.NoError(t, cache.StoreRefreshToken(ctx, "hash-old", &service.RefreshTokenData{
		UserID: 3, FamilyID: familyID, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}, 10*time.Minute))
	consumer, ok := cache.(service.AtomicRefreshTokenConsumer)
	require.True(t, ok)
	_, reused, err := consumer.ConsumeRefreshToken(ctx, "hash-old")
	require.NoError(t, err)
	require.False(t, reused)
	// A successor that was stored before reuse detection must be deleted by the
	// family revoke script as well.
	require.NoError(t, cache.StoreRefreshToken(ctx, "hash-successor", &service.RefreshTokenData{
		UserID: 3, FamilyID: familyID, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}, 10*time.Minute))
	require.NoError(t, cache.DeleteTokenFamily(ctx, familyID))

	require.ErrorIs(t, cache.StoreRefreshToken(ctx, "hash-late", &service.RefreshTokenData{
		UserID: 3, FamilyID: familyID, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}, 10*time.Minute), service.ErrRefreshTokenFamilyRevoked)
	require.ErrorIs(t, cache.AddToFamilyTokenSet(ctx, familyID, "hash-late", 10*time.Minute), service.ErrRefreshTokenFamilyRevoked)
	_, err = cache.GetRefreshToken(ctx, "hash-successor")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
	hashes, err := cache.GetFamilyTokenHashes(ctx, familyID)
	require.NoError(t, err)
	require.Empty(t, hashes, "revocation sentinel must stay hidden from callers")

	markerTTL, err := client.PTTL(ctx, tokenFamilyKey(familyID)).Result()
	require.NoError(t, err)
	require.Greater(t, markerTTL, time.Duration(0))
}
