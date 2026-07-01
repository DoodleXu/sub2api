//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type subscriptionInvalidationCacheStub struct {
	billingCacheWorkerStub

	mu            sync.Mutex
	handlers      []func(string)
	published     []string
	invalidated   []string
	invalidateErr error
	publishErr    error
	subscribeErr  error
}

func (s *subscriptionInvalidationCacheStub) InvalidateSubscriptionCache(_ context.Context, userID, groupID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated = append(s.invalidated, subCacheKey(userID, groupID))
	return s.invalidateErr
}

func (s *subscriptionInvalidationCacheStub) PublishSubscriptionCacheInvalidation(_ context.Context, cacheKey string) error {
	s.mu.Lock()
	if s.publishErr != nil {
		s.mu.Unlock()
		return s.publishErr
	}
	s.published = append(s.published, cacheKey)
	handlers := append([]func(string){}, s.handlers...)
	s.mu.Unlock()

	for _, handler := range handlers {
		handler(cacheKey)
	}
	return nil
}

func (s *subscriptionInvalidationCacheStub) SubscribeSubscriptionCacheInvalidation(_ context.Context, handler func(cacheKey string)) error {
	if s.subscribeErr != nil {
		return s.subscribeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append(s.handlers, handler)
	return nil
}

func (s *subscriptionInvalidationCacheStub) publishedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.published...)
}

func newSubscriptionCacheTestConfig() *config.Config {
	return &config.Config{
		SubscriptionCache: config.SubscriptionCacheConfig{
			L1Size:       16,
			L1TTLSeconds: 60,
		},
	}
}

func TestAdminResetQuotaPublishesInvalidationToPeerL1(t *testing.T) {
	repo := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{
			ID:            1,
			UserID:        10,
			GroupID:       20,
			Status:        SubscriptionStatusActive,
			ExpiresAt:     time.Now().Add(time.Hour),
			DailyUsageUSD: 7,
		},
	}
	cache := &subscriptionInvalidationCacheStub{}
	billingSvc := &BillingCacheService{cache: cache}
	svcA := NewSubscriptionService(groupRepoNoop{}, repo, billingSvc, nil, newSubscriptionCacheTestConfig())
	svcB := NewSubscriptionService(groupRepoNoop{}, repo, billingSvc, nil, newSubscriptionCacheTestConfig())
	t.Cleanup(svcA.Stop)
	t.Cleanup(svcB.Stop)

	warmed, err := svcB.GetActiveSubscription(context.Background(), 10, 20)
	require.NoError(t, err)
	require.Equal(t, 7.0, warmed.DailyUsageUSD)
	svcB.subCacheL1.Wait()
	require.Equal(t, 1, repo.getActiveCalls)

	reset, err := svcA.AdminResetQuota(context.Background(), 1, true, false, false)
	require.NoError(t, err)
	require.Equal(t, 0.0, reset.DailyUsageUSD)
	require.Contains(t, cache.publishedKeys(), "sub:10:20")

	refetched, err := svcB.GetActiveSubscription(context.Background(), 10, 20)
	require.NoError(t, err)
	require.Equal(t, 0.0, refetched.DailyUsageUSD)
	require.Equal(t, 2, repo.getActiveCalls, "peer L1 should be invalidated by the published cache key")
}

func TestRevokeSubscriptionIgnoresCacheInvalidationFailureAfterDelete(t *testing.T) {
	repo := &revokeCacheUserSubRepoStub{
		sub: &UserSubscription{
			ID:        2,
			UserID:    30,
			GroupID:   40,
			Status:    SubscriptionStatusActive,
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	cache := &subscriptionInvalidationCacheStub{
		invalidateErr: errors.New("redis unavailable"),
		publishErr:    errors.New("pubsub unavailable"),
	}
	billingSvc := &BillingCacheService{cache: cache}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, billingSvc, nil, newSubscriptionCacheTestConfig())
	t.Cleanup(svc.Stop)

	err := svc.RevokeSubscription(context.Background(), 2)
	require.NoError(t, err)
	require.True(t, repo.deleted, "DB revoke already succeeded and should not be reported as failed by cache cleanup")
}
