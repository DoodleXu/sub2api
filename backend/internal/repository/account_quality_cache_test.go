package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSharedQualityDedupCircuitAndLease(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	a := &tempUnschedCache{rdb: rdb}
	b := &tempUnschedCache{rdb: rdb}
	ctx := context.Background()
	key := "account_quality:{123}:0:model"
	now := time.Now()
	server.SetTime(now)
	sample := service.AccountQualitySample{ID: "same-attempt", At: now.UnixMilli(), Failed: true, Transient: true}
	_, err := a.RecordAccountQuality(ctx, key, sample)
	require.NoError(t, err)
	_, err = b.RecordAccountQuality(ctx, key, sample)
	require.NoError(t, err)
	states, err := a.ReadAccountQuality(ctx, []string{key})
	require.NoError(t, err)
	require.Len(t, states[key].Samples, 1)
	for i := 0; i < 2; i++ {
		sample.ID = fmt.Sprint(i)
		sample.At = now.Add(time.Duration(i+1) * time.Second).UnixMilli()
		_, err = a.RecordAccountQuality(ctx, key, sample)
		require.NoError(t, err)
	}
	ok, err := b.AcquireAccountQualityPermit(ctx, key, "blocked", false)
	require.NoError(t, err)
	require.False(t, ok)
	server.SetTime(now.Add(2 * time.Minute))
	ok, err = a.AcquireAccountQualityPermit(ctx, key, "recovery-a", true)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = b.AcquireAccountQualityPermit(ctx, key, "recovery-b", true)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, b.ReleaseAccountQualityPermit(ctx, key, "wrong-owner"))
	ok, err = b.AcquireAccountQualityPermit(ctx, key, "recovery-b", true)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, a.ReleaseAccountQualityPermit(ctx, key, "recovery-a"))
	ok, err = b.AcquireAccountQualityPermit(ctx, key, "recovery-b", true)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, a.SetAccountBalanceBlock(ctx, 123, 0))
	ok, err = b.AcquireAccountQualityPermit(ctx, key, "debt", false)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = b.AcquireAccountQualityPermit(ctx, "account_quality:{123}:2:model", "resumed", false)
	require.NoError(t, err)
	require.True(t, ok, "manual resume generation must not inherit old debt fence")
}
func TestSharedQualityConcurrentWriters(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &tempUnschedCache{rdb: rdb}
	ctx := context.Background()
	key := "account_quality:{321}:0:model"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, err := cache.RecordAccountQuality(ctx, key, service.AccountQualitySample{ID: fmt.Sprintf("%d:%d", w, i), At: time.Now().UnixMilli()})
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	states, err := cache.ReadAccountQuality(ctx, []string{key})
	require.NoError(t, err)
	require.Len(t, states[key].Samples, 40)
}

func TestSharedQualityAccountCircuitFencesOtherModelsAndOneRecovery(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &tempUnschedCache{rdb: rdb}
	ctx := context.Background()
	now := time.Now()
	server.SetTime(now)
	for i := 0; i < 3; i++ {
		_, err := cache.RecordAccountQuality(ctx, "account_quality:{90}:0:account", service.AccountQualitySample{ID: fmt.Sprint(i), At: now.Add(time.Duration(i) * time.Second).UnixMilli(), Failed: true, Transient: true})
		require.NoError(t, err)
	}
	allowed, err := cache.AcquireAccountQualityPermit(ctx, "account_quality:{90}:0:model-b", "b", false)
	require.NoError(t, err)
	require.False(t, allowed, "account fault must fence models with no local fault samples")
	server.SetTime(now.Add(2 * time.Minute))
	allowed, err = cache.AcquireAccountQualityPermit(ctx, "account_quality:{90}:0:model-b", "b", false)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = cache.AcquireAccountQualityPermit(ctx, "account_quality:{90}:0:model-c", "c", false)
	require.NoError(t, err)
	require.False(t, allowed, "only one cross-model account recovery can be in flight")
	require.NoError(t, cache.ReleaseAccountQualityPermit(ctx, "account_quality:{90}:0:model-b", "b"))
	require.NoError(t, cache.ReleaseAccountQualityPermit(ctx, "account_quality:{90}:0:account", "b"))
	allowed, err = cache.AcquireAccountQualityPermit(ctx, "account_quality:{90}:0:model-c", "c", false)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestSharedQualityLeaseRenewalAndBalanceDispatchFence(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &tempUnschedCache{rdb: rdb}
	ctx := context.Background()
	key := "account_quality:{91}:0:model"
	allowed, err := cache.AcquireAccountQualityPermit(ctx, key, "owner", true)
	require.NoError(t, err)
	require.True(t, allowed)
	server.FastForward(14 * time.Minute)
	require.NoError(t, cache.RenewAccountQualityPermit(ctx, key, "owner"))
	server.FastForward(14 * time.Minute)
	allowed, err = cache.AcquireAccountQualityPermit(ctx, key, "other", true)
	require.NoError(t, err)
	require.False(t, allowed, "long streams retain the exclusive probe lease")
	require.NoError(t, cache.SetAccountBalanceBlock(ctx, 91, 0))
	blocked, err := cache.HasAccountBalanceBlock(ctx, 91, 0)
	require.NoError(t, err)
	require.True(t, blocked)
	blocked, err = cache.HasAccountBalanceBlock(ctx, 91, 1)
	require.NoError(t, err)
	require.False(t, blocked)
}
