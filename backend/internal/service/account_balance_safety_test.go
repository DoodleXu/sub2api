package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gorilla/websocket"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type balanceSafetyRepo struct {
	AccountRepository
	mu      sync.Mutex
	current Account
	pauses  int
}

func (r *balanceSafetyRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.current
	a.Extra = make(map[string]any, len(r.current.Extra))
	for k, v := range r.current.Extra {
		a.Extra[k] = v
	}
	return &a, nil
}
func (r *balanceSafetyRepo) PauseAccountForInsufficientBalance(_ context.Context, a *Account, p AccountBalancePause) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if AccountSchedulingGeneration(&r.current) != AccountSchedulingGeneration(a) || r.current.Extra[AccountBalancePauseKey] != nil {
		return false, nil
	}
	r.current.Schedulable = false
	r.current.Extra[AccountBalancePauseKey] = p
	r.current.Extra[AccountSchedulingGenerationKey] = AccountSchedulingGeneration(a) + 1
	r.pauses++
	return true, nil
}
func TestDebtStopsPoolAccountBeforeCustomPolicyAndCannotUndoManualResume(t *testing.T) {
	repo := &balanceSafetyRepo{current: Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{}, Credentials: map[string]any{"pool_mode": true, "custom_error_codes_enabled": true, "custom_error_codes": []any{500}}}}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model"})
	before, err := repo.GetByID(ctx, 77)
	require.NoError(t, err)
	const body = `{"error":{"message":"Insufficient account balance"}}`
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); svc.HandleUpstreamError(ctx, before, 403, nil, []byte(body)) }()
	}
	wg.Wait()
	require.Equal(t, 1, repo.pauses)
	_, _, err = svc.BeginAccountQualityAttempt(ctx, before)
	require.Error(t, err)
	repo.mu.Lock()
	repo.current.Schedulable = true
	delete(repo.current.Extra, AccountBalancePauseKey)
	repo.current.Extra[AccountSchedulingGenerationKey] = int64(2)
	repo.mu.Unlock()
	require.True(t, svc.HandleAccountBalanceError(ctx, before, 403, []byte(body)))
	after, err := repo.GetByID(ctx, 77)
	require.NoError(t, err)
	require.True(t, after.Schedulable)
	require.Equal(t, 1, repo.pauses)
	_, attempt, err := svc.BeginAccountQualityAttempt(ctx, after)
	require.NoError(t, err)
	attempt.Abort()
}
func TestModelRateLimitNeverCreatesFinancialPause(t *testing.T) {
	repo := &balanceSafetyRepo{current: Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{}, Credentials: map[string]any{"pool_mode": true}}}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	a, err := repo.GetByID(context.Background(), 77)
	require.NoError(t, err)
	svc.HandleUpstreamError(context.Background(), a, 429, nil, []byte(`{"error":{"message":"model quota will reset in 60 seconds"}}`))
	require.Zero(t, repo.pauses)
	require.True(t, repo.current.Schedulable)
}

func TestFinancialFailurePrecedesPassthroughAndCustomErrorPolicy(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "images", "grok", "messages"} {
		t.Run(path, func(t *testing.T) {
			repo := &balanceSafetyRepo{current: Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{}, Credentials: map[string]any{"pool_mode": true, "custom_error_codes_enabled": true, "custom_error_codes": []any{500}}}}
			rl := NewRateLimitService(repo, nil, nil, nil, nil)
			svc := &OpenAIGatewayService{rateLimitService: rl}
			a, _ := repo.GetByID(context.Background(), 77)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte(`{"error":{"message":"Insufficient account balance"}}`)
			resp := &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}
			var err error
			switch path {
			case "responses":
				_, err = svc.handleErrorResponse(context.Background(), resp, c, a, nil)
			case "passthrough":
				err = svc.handleErrorResponsePassthroughBodyMode(context.Background(), resp, c, a, nil, body, true)
			case "images":
				_, err = svc.handleOpenAIImagesErrorResponse(context.Background(), resp, c, a)
			case "grok":
				_, err = svc.handleGrokMediaErrorResponse(context.Background(), resp, c, a, "x-request-id", "model")
			case "messages":
				_, err = (&GatewayService{rateLimitService: rl}).handleErrorResponse(context.Background(), resp, c, a)
			}
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.True(t, failure.ShouldRetryNextAccount())
			require.False(t, failure.RetryableOnSameAccount)
			require.False(t, c.Writer.Written(), "debt must be classified before committing the client response")
			require.Equal(t, 1, repo.pauses)
		})
	}
}

func TestDebtWebSocketStopsAccountWithoutRetryOrClientOutput(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		var body map[string]any
		if err := conn.ReadJSON(&body); err != nil {
			t.Errorf("read: %v", err)
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"code": "insufficient_balance", "message": "Insufficient account balance"}})
	}))
	defer upstream.Close()
	repo := &balanceSafetyRepo{current: Account{ID: 78, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "sk-test", "base_url": upstream.URL}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	rl := NewRateLimitService(repo, nil, cfg, nil, nil)
	svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: rl, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector()}
	a, err := repo.GetByID(context.Background(), 78)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.3-codex"}`))
	_, err = svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-5.3-codex","stream":true,"input":"test"}`))
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.ShouldRetryNextAccount())
	require.Equal(t, int32(1), attempts.Load())
	require.Equal(t, 1, repo.pauses)
	require.False(t, c.Writer.Written())
}

type shadowBalanceSafetyRepo struct {
	*balanceSafetyRepo
	child Account
}

func (r *shadowBalanceSafetyRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if id == r.child.ID {
		child := r.child
		return &child, nil
	}
	return r.balanceSafetyRepo.GetByID(ctx, id)
}
func TestDebtShadowPausesCredentialOwnerAndPreservesManualParentPauseBehavior(t *testing.T) {
	parentID := int64(80)
	repo := &shadowBalanceSafetyRepo{balanceSafetyRepo: &balanceSafetyRepo{current: Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: false, Extra: map[string]any{}, Credentials: map[string]any{"access_token": "test"}}}, child: Account{ID: 81, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	ctx, attempt, err := svc.BeginAccountQualityAttempt(WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "spark"}), &repo.child)
	require.NoError(t, err, "manual parent scheduling pause must not block the independent shadow")
	require.True(t, svc.HandleAccountBalanceError(ctx, &repo.child, 403, []byte(`{"error":{"message":"Insufficient account balance"}}`)))
	require.Error(t, attempt.Finish(nil, &UpstreamFailoverError{StatusCode: 403, ResponseBody: []byte(`{"error":{"message":"Insufficient account balance"}}`)}))
	require.Equal(t, 1, repo.pauses)
	require.NotNil(t, repo.current.Extra[AccountBalancePauseKey])
	require.Nil(t, repo.child.Extra[AccountBalancePauseKey])
	require.Error(t, svc.ValidateAccountDispatch(context.Background(), &repo.child), "financial pause of the credential owner must fence every shadow")
}

func TestDebtWebSocketHTTPBridgeStopsBeforeClientOutput(t *testing.T) {
	for _, status := range []int{403, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			repo := &balanceSafetyRepo{current: Account{ID: 93, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{}, Credentials: map[string]any{"api_key": "test"}}}
			rl := NewRateLimitService(repo, nil, nil, nil, nil)
			body := `{"error":{"message":"Insufficient account balance"}}`
			if status == 200 {
				body = "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Insufficient account balance\"}}}\n\n"
			}
			svc := &OpenAIGatewayService{rateLimitService: rl, cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, httpUpstream: &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}}}
			a, _ := repo.GetByID(context.Background(), 93)
			ctx, attempt, err := rl.BeginAccountQualityAttempt(WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "gpt-5.5", Transport: "websocket_http_bridge", Stream: true}), a)
			require.NoError(t, err)
			defer attempt.Abort()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5.5","stream":true,"input":"test"}`)
			writes := 0
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, a, "test", payload, len(payload), "gpt-5.5", "", "", "", "", 1, func([]byte) error { writes++; return nil })
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Error(t, attempt.Finish(result, err))
			require.Zero(t, writes)
			require.Equal(t, 1, repo.pauses)
		})
	}
}
