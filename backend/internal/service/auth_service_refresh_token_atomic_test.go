//go:build unit

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Embedding the full interface keeps this test focused on RefreshTokenPair;
// only GetByID is reached by the code under test.
type refreshPairUserRepo struct {
	service.UserRepository
	mu   sync.Mutex
	user *service.User
}

func (r *refreshPairUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.user == nil || r.user.ID != id {
		return nil, service.ErrUserNotFound
	}
	cloned := *r.user
	return &cloned, nil
}

type blockingRefreshTokenCache struct {
	service.RefreshTokenCache
	consumer     service.AtomicRefreshTokenConsumer
	blockStores  atomic.Bool
	storeStarted chan struct{}
	storeRelease chan struct{}
	once         sync.Once
}

func (c *blockingRefreshTokenCache) StoreRefreshToken(ctx context.Context, hash string, data *service.RefreshTokenData, ttl time.Duration) error {
	if c.blockStores.Load() {
		c.once.Do(func() { close(c.storeStarted) })
		select {
		case <-c.storeRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.RefreshTokenCache.StoreRefreshToken(ctx, hash, data, ttl)
}

func (c *blockingRefreshTokenCache) ConsumeRefreshToken(ctx context.Context, hash string) (*service.RefreshTokenData, bool, error) {
	return c.consumer.ConsumeRefreshToken(ctx, hash)
}

func newAtomicRefreshRedis(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.NewMiniRedis()
	if err := server.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newAtomicRefreshAuthService(repo service.UserRepository, cache service.RefreshTokenCache) *service.AuthService {
	cfg := &config.Config{JWT: config.JWTConfig{
		Secret:                   "atomic-refresh-test-secret",
		AccessTokenExpireMinutes: 10,
		RefreshTokenExpireDays:   30,
	}}
	auth := service.NewAuthService(
		nil,
		repo,
		nil,
		cache,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	return auth
}

func TestRefreshTokenPairConcurrentReuseRevokesFamilyBeforeWinnerMints(t *testing.T) {
	ctx := context.Background()
	user := &service.User{ID: 21, Email: "atomic@example.com", PasswordHash: "password-hash", Role: service.RoleUser, Status: service.StatusActive, TokenVersionResolved: true, TokenVersion: 3}
	repo := &refreshPairUserRepo{user: user}
	client := newAtomicRefreshRedis(t)
	baseCache := repository.NewRefreshTokenCache(client)
	blocking := &blockingRefreshTokenCache{
		RefreshTokenCache: baseCache,
		consumer:          baseCache.(service.AtomicRefreshTokenConsumer),
		storeStarted:      make(chan struct{}),
		storeRelease:      make(chan struct{}),
	}
	auth := newAtomicRefreshAuthService(repo, blocking)

	pair, err := auth.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	require.NotEmpty(t, pair.RefreshToken)

	blocking.blockStores.Store(true)
	firstResult := make(chan error, 1)
	go func() {
		_, refreshErr := auth.RefreshTokenPair(ctx, pair.RefreshToken)
		firstResult <- refreshErr
	}()
	select {
	case <-blocking.storeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh did not reach successor token storage")
	}

	// The second call consumes the tombstone, revokes the family, and returns
	// reuse. The first call is still blocked before token generation, so its
	// atomic StoreRefreshToken must observe the family sentinel and fail closed.
	_, secondErr := auth.RefreshTokenPair(ctx, pair.RefreshToken)
	require.ErrorIs(t, secondErr, service.ErrRefreshTokenReused)
	close(blocking.storeRelease)
	select {
	case firstErr := <-firstResult:
		require.ErrorIs(t, firstErr, service.ErrRefreshTokenReused)
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh did not finish")
	}

	// The old token and any successor must not remain usable after family
	// revocation. The marker is intentionally visible only through the family
	// implementation, not as a caller-facing token hash.
	claims, err := auth.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	members, err := client.SMembers(ctx, "token_family:"+claims.SessionID).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"__sub2api_family_revoked__"}, members)
}

func TestBrowserRefreshReplayOfConsumedDesktopTokenDoesNotRevokeFamily(t *testing.T) {
	ctx := context.Background()
	user := &service.User{ID: 22, Email: "desktop-replay@example.com", PasswordHash: "password-hash", Role: service.RoleUser, Status: service.StatusActive, TokenVersionResolved: true, TokenVersion: 4}
	repo := &refreshPairUserRepo{user: user}
	client := newAtomicRefreshRedis(t)
	cache := repository.NewRefreshTokenCache(client)
	auth := newAtomicRefreshAuthService(repo, cache)

	initial, err := auth.GenerateDeviceTokenPair(ctx, user, "desktop-family", "desktop-device", []string{"profile"}, service.DesktopAudience)
	require.NoError(t, err)
	rotated, err := auth.RefreshTokenPair(ctx, initial.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, rotated.RefreshToken)

	// The old desktop token is now represented only by the short-lived replay
	// tombstone. The legacy browser endpoint cannot supply the DPoP proof and
	// must reject it without revoking the still-valid successor family.
	_, err = auth.RefreshTokenPairForBrowser(ctx, initial.RefreshToken)
	require.ErrorIs(t, err, service.ErrDesktopRefreshRequiresDPoP)
	_, err = cache.GetRefreshToken(ctx, hashRefreshTokenForTest(rotated.RefreshToken))
	require.NoError(t, err)
}

func hashRefreshTokenForTest(token string) string {
	// Keep the test independent from service internals while addressing the
	// repository by the same SHA-256 token hash used in production.
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
