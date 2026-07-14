package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCyberBlockTestCtx(headers map[string]string, body string) (*gin.Context, []byte) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/openai/v1/responses", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c, []byte(body)
}

// TestCyberSessionBlockKey verifies F5a key derivation: explicit session signals
// only (header session_id/conversation_id or body prompt_cache_key), apiKey
// isolated, and EMPTY when no explicit signal (no content-derived fallback —
// "不退化" decision).
func TestCyberSessionBlockKey(t *testing.T) {
	c1, b1 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	k1 := CyberSessionBlockKey(101, c1, b1)
	require.NotEmpty(t, k1)

	// Same session, different apiKey → different key (isolation).
	c2, b2 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	require.NotEqual(t, k1, CyberSessionBlockKey(202, c2, b2))

	// Same session + same apiKey → stable key.
	c3, b3 := newCyberBlockTestCtx(map[string]string{"session_id": "sess-abc"}, `{}`)
	require.Equal(t, k1, CyberSessionBlockKey(101, c3, b3))

	// prompt_cache_key in body counts as explicit.
	c4, b4 := newCyberBlockTestCtx(nil, `{"prompt_cache_key":"pck-1"}`)
	require.NotEmpty(t, CyberSessionBlockKey(101, c4, b4))

	// No explicit signal → empty key → caller must skip blocking entirely.
	c5, b5 := newCyberBlockTestCtx(nil, `{"input":"hello world"}`)
	require.Empty(t, CyberSessionBlockKey(101, c5, b5))
	require.NotEmpty(t, CyberSessionBlockKeyWithFallback(101, c5, b5, "alpha-search-id"))
	require.Empty(t, CyberSessionBlockKey(101, c5, b5), "endpoint fallback must not change the generic helper")

	// Existing explicit signals keep priority over an endpoint-specific fallback.
	require.Equal(t, k1, CyberSessionBlockKeyWithFallback(101, c1, b1, "different-alpha-id"))

	// conversation_id header counts as explicit; key is stable and non-empty.
	c6, b6 := newCyberBlockTestCtx(map[string]string{"conversation_id": "conv-xyz"}, `{}`)
	k6 := CyberSessionBlockKey(101, c6, b6)
	require.NotEmpty(t, k6)
	c6b, b6b := newCyberBlockTestCtx(map[string]string{"conversation_id": "conv-xyz"}, `{}`)
	require.Equal(t, k6, CyberSessionBlockKey(101, c6b, b6b), "conversation_id key must be stable")
}

// --- fakes ---

type fakeCyberBlockStore struct {
	blocked map[string]bool
}

var _ CyberSessionBlockStore = (*fakeCyberBlockStore)(nil)

func (f *fakeCyberBlockStore) SetCyberSessionBlocked(_ context.Context, key string, _ time.Duration) error {
	if f.blocked == nil {
		f.blocked = map[string]bool{}
	}
	f.blocked[key] = true
	return nil
}

func (f *fakeCyberBlockStore) IsCyberSessionBlocked(_ context.Context, key string) (bool, error) {
	return f.blocked[key], nil
}

// fakeSettingRepo is a minimal SettingRepository stub for unit tests.
// Only GetValue is exercised by GetCyberSessionBlockRuntime; all other methods
// panic so accidental calls are caught immediately.
type fakeSettingRepo struct {
	vals map[string]string
}

func (r *fakeSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	v, ok := r.vals[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (r *fakeSettingRepo) Get(_ context.Context, _ string) (*Setting, error) {
	panic("fakeSettingRepo.Get not implemented")
}
func (r *fakeSettingRepo) Set(_ context.Context, _, _ string) error {
	panic("fakeSettingRepo.Set not implemented")
}
func (r *fakeSettingRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	panic("fakeSettingRepo.GetMultiple not implemented")
}
func (r *fakeSettingRepo) SetMultiple(_ context.Context, _ map[string]string) error {
	panic("fakeSettingRepo.SetMultiple not implemented")
}
func (r *fakeSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	panic("fakeSettingRepo.GetAll not implemented")
}
func (r *fakeSettingRepo) Delete(_ context.Context, _ string) error {
	panic("fakeSettingRepo.Delete not implemented")
}

var _ SettingRepository = (*fakeSettingRepo)(nil)

type blockingRuntimeSettingRepo struct {
	SettingRepository
	started chan struct{}
	release chan struct{}
}

func (r *blockingRuntimeSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key == SettingKeyCyberSessionBlockEnabled {
		select {
		case r.started <- struct{}{}:
		default:
		}
		select {
		case <-r.release:
			return "true", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if key == SettingKeyCyberSessionBlockTTLSeconds {
		return "60", nil
	}
	return "", ErrSettingNotFound
}

type runtimeSettingResult struct {
	enabled bool
	ttl     time.Duration
	elapsed time.Duration
}

func newBlockingRuntimeSettingRepo() (*blockingRuntimeSettingRepo, func()) {
	repo := &blockingRuntimeSettingRepo{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	return repo, func() { releaseOnce.Do(func() { close(repo.release) }) }
}

func TestGetCyberSessionBlockRuntimeCallerDeadlineDoesNotCancelRefresh(t *testing.T) {
	repo, release := newBlockingRuntimeSettingRepo()
	defer release()
	svc := &SettingService{settingRepo: repo}

	callerResult := make(chan runtimeSettingResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		startedAt := time.Now()
		enabled, ttl := svc.GetCyberSessionBlockRuntime(ctx)
		callerResult <- runtimeSettingResult{enabled: enabled, ttl: ttl, elapsed: time.Since(startedAt)}
	}()

	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("runtime refresh did not start")
	}

	var caller runtimeSettingResult
	select {
	case caller = <-callerResult:
	case <-time.After(500 * time.Millisecond):
		release()
		<-callerResult
		t.Fatal("caller waited for the detached runtime refresh past its own deadline")
	}
	require.False(t, caller.enabled)
	require.Equal(t, time.Hour, caller.ttl)
	require.Less(t, caller.elapsed, 500*time.Millisecond)

	// Caller timeout only stops waiting. The detached refresh still completes and
	// populates the cache for the next request.
	release()
	enabled, ttl := svc.GetCyberSessionBlockRuntime(context.Background())
	require.True(t, enabled)
	require.Equal(t, time.Minute, ttl)
}

func TestGetCyberSessionBlockRuntimeDuplicateCallerHonorsOwnDeadline(t *testing.T) {
	repo, release := newBlockingRuntimeSettingRepo()
	defer release()

	svc := &SettingService{settingRepo: repo}
	firstResult := make(chan runtimeSettingResult, 1)
	go func() {
		enabled, ttl := svc.GetCyberSessionBlockRuntime(context.Background())
		firstResult <- runtimeSettingResult{enabled: enabled, ttl: ttl}
	}()

	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("initial runtime refresh did not start")
	}

	duplicateResult := make(chan runtimeSettingResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		startedAt := time.Now()
		enabled, ttl := svc.GetCyberSessionBlockRuntime(ctx)
		duplicateResult <- runtimeSettingResult{enabled: enabled, ttl: ttl, elapsed: time.Since(startedAt)}
	}()

	var duplicate runtimeSettingResult
	select {
	case duplicate = <-duplicateResult:
	case <-time.After(500 * time.Millisecond):
		release()
		<-duplicateResult
		t.Fatal("duplicate caller waited for the existing singleflight refresh past its own deadline")
	}
	require.False(t, duplicate.enabled)
	require.Equal(t, time.Hour, duplicate.ttl)
	require.Less(t, duplicate.elapsed, 500*time.Millisecond)

	// The timed-out waiter must not cancel the shared refresh. Once released, the
	// original caller completes and the refreshed value remains available in cache.
	release()
	select {
	case first := <-firstResult:
		require.True(t, first.enabled)
		require.Equal(t, time.Minute, first.ttl)
	case <-time.After(time.Second):
		t.Fatal("initial runtime refresh did not finish after release")
	}
	enabled, ttl := svc.GetCyberSessionBlockRuntime(context.Background())
	require.True(t, enabled)
	require.Equal(t, time.Minute, ttl)
}

// comboCacheAndStore implements both GatewayCache (no-op stubs) and
// CyberSessionBlockStore (delegates to fakeCyberBlockStore) so it can be
// injected as s.cache and successfully type-asserted to CyberSessionBlockStore.
type comboCacheAndStore struct {
	store fakeCyberBlockStore
}

var _ GatewayCache = (*comboCacheAndStore)(nil)
var _ CyberSessionBlockStore = (*comboCacheAndStore)(nil)

func (c *comboCacheAndStore) GetSessionAccountID(_ context.Context, _ int64, _ string) (int64, error) {
	return 0, errors.New("stub")
}
func (c *comboCacheAndStore) SetSessionAccountID(_ context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	return nil
}
func (c *comboCacheAndStore) RefreshSessionTTL(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}
func (c *comboCacheAndStore) DeleteSessionAccountID(_ context.Context, _ int64, _ string) error {
	return nil
}
func (c *comboCacheAndStore) SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error {
	return c.store.SetCyberSessionBlocked(ctx, key, ttl)
}
func (c *comboCacheAndStore) IsCyberSessionBlocked(ctx context.Context, key string) (bool, error) {
	return c.store.IsCyberSessionBlocked(ctx, key)
}

// --- tests ---

// TestIsCyberSessionBlocked_EmptyKeyAndNilService covers the fail-open paths:
// empty key, nil service, store missing → always false / no panic.
func TestIsCyberSessionBlocked_EmptyKeyAndNilService(t *testing.T) {
	var nilSvc *OpenAIGatewayService
	require.False(t, nilSvc.IsCyberSessionBlocked(context.Background(), "k"))
	require.NotPanics(t, func() { nilSvc.MarkCyberSessionBlocked(context.Background(), "k") })

	svc := &OpenAIGatewayService{}
	require.False(t, svc.IsCyberSessionBlocked(context.Background(), ""))
	require.False(t, svc.IsCyberSessionBlocked(context.Background(), "k"), "no store + no settings → fail-open false")
}

// TestCyberSessionBlock_RoundTrip exercises the type-assertion success path:
// mark a session blocked via a combo cache+store, then confirm IsCyberSessionBlocked
// returns true, and an unrelated key returns false.
func TestCyberSessionBlock_RoundTrip(t *testing.T) {
	// SettingService with only settingRepo set — GetCyberSessionBlockRuntime needs
	// nothing else (cfg/proxyRepo/etc. are not touched by this code path).
	settingSvc := &SettingService{
		settingRepo: &fakeSettingRepo{
			vals: map[string]string{
				SettingKeyCyberSessionBlockEnabled:    "true",
				SettingKeyCyberSessionBlockTTLSeconds: "60",
			},
		},
	}

	combo := &comboCacheAndStore{}
	svc := &OpenAIGatewayService{
		cache:          combo,
		settingService: settingSvc,
	}

	ctx := context.Background()
	const testKey = "deadbeef1234"

	// Before marking: not blocked.
	require.False(t, svc.IsCyberSessionBlocked(ctx, testKey))

	// Mark as blocked.
	require.True(t, svc.MarkCyberSessionBlocked(ctx, testKey))

	// After marking: blocked.
	require.True(t, svc.IsCyberSessionBlocked(ctx, testKey))

	// Different key: still not blocked.
	require.False(t, svc.IsCyberSessionBlocked(ctx, "other-key"))
}
