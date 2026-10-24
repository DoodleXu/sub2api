package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Model migration249: token persistence advances the durable generation, while
// the account value supplied to the provider remains the pre-refresh snapshot.
type qualityRefreshRepo struct {
	*balanceSafetyRepo
	afterWrite func(*Account)
}

func (r *qualityRefreshRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	payload, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	var durable map[string]any
	if err := json.Unmarshal(payload, &durable); err != nil {
		return err
	}
	r.current.Credentials = durable
	r.current.Extra[AccountSchedulingGenerationKey] = AccountSchedulingGeneration(&r.current) + 1
	if r.afterWrite != nil {
		r.afterWrite(&r.current)
	}
	return nil
}

type qualityRefreshExecutor struct{}

func (qualityRefreshExecutor) CanRefresh(*Account) bool                  { return true }
func (qualityRefreshExecutor) NeedsRefresh(*Account, time.Duration) bool { return true }
func (qualityRefreshExecutor) CacheKey(*Account) string                  { return "quality-refresh" }
func (qualityRefreshExecutor) Refresh(context.Context, *Account) (map[string]any, error) {
	return map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}, nil
}

func newQualityRefreshRepo() *qualityRefreshRepo {
	return &qualityRefreshRepo{balanceSafetyRepo: &balanceSafetyRepo{current: Account{
		ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{}, Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh"},
	}}}
}

func TestQualityOAuthRefreshAuthorizesOnlyItsRequest(t *testing.T) {
	for _, acquireBeforeAttempt := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "websocket_token_before_turn"}[acquireBeforeAttempt], func(t *testing.T) {
			repo := newQualityRefreshRepo()
			rl := NewRateLimitService(repo, nil, nil, nil, nil)
			ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "gpt-5.5"})
			account, err := repo.GetByID(ctx, 501)
			require.NoError(t, err)
			if !acquireBeforeAttempt {
				var attempt *accountQualityAttempt
				ctx, attempt, err = rl.BeginAccountQualityAttempt(ctx, account)
				require.NoError(t, err)
				defer attempt.Abort()
			}
			result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(ctx, account, qualityRefreshExecutor{}, time.Minute)
			require.NoError(t, err)
			require.True(t, result.Refreshed)
			require.EqualValues(t, 1, AccountSchedulingGeneration(result.Account))
			require.Zero(t, AccountSchedulingGeneration(account), "do not mutate a shared scheduler account")
			if acquireBeforeAttempt {
				var attempt *accountQualityAttempt
				ctx, attempt, err = rl.BeginAccountQualityAttempt(ctx, account)
				require.NoError(t, err)
				defer attempt.Abort()
			}
			require.NoError(t, ValidateAccountQualityDispatch(ctx))
			require.Error(t, rl.ValidateAccountDispatch(context.Background(), account), "another old request has no refresh authorization")
			require.True(t, rl.HandleAccountBalanceError(ctx, account, 403, []byte(`{"error":{"code":"insufficient_balance"}}`)))
			require.Equal(t, 1, repo.pauses, "post-refresh debt must pause the durable credential generation")
			require.Error(t, ValidateAccountQualityDispatch(ctx), "refresh cannot lift a later debt pause")
		})
	}
}

func TestQualityOAuthRefreshDoesNotAuthorizeConcurrentAdminChanges(t *testing.T) {
	cases := map[string]func(*Account){
		"manual_resume": func(a *Account) { a.Extra[AccountSchedulingGenerationKey] = AccountSchedulingGeneration(a) + 1 },
		"replaced_credentials": func(a *Account) {
			a.Credentials = map[string]any{"access_token": "admin-replacement"}
			a.Extra[AccountSchedulingGenerationKey] = AccountSchedulingGeneration(a) + 1
		},
		"paused": func(a *Account) {
			a.Schedulable = false
			a.Extra[AccountSchedulingGenerationKey] = AccountSchedulingGeneration(a) + 1
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newQualityRefreshRepo()
			repo.afterWrite = change
			rl := NewRateLimitService(repo, nil, nil, nil, nil)
			account, err := repo.GetByID(context.Background(), 501)
			require.NoError(t, err)
			ctx, attempt, err := rl.BeginAccountQualityAttempt(context.Background(), account)
			require.NoError(t, err)
			defer attempt.Abort()
			_, err = NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(ctx, account, qualityRefreshExecutor{}, time.Minute)
			require.Error(t, err)
			require.Error(t, ValidateAccountQualityDispatch(ctx))
		})
	}
}

func TestQualityOAuthRefreshUpdatesShadowCredentialOwner(t *testing.T) {
	parent := newQualityRefreshRepo()
	parent.current.Schedulable = false // independent shadow scheduling stays allowed
	parentID := parent.current.ID
	repo := &shadowQualityRefreshRepo{qualityRefreshRepo: parent, child: Account{
		ID: 502, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
	}}
	rl := NewRateLimitService(repo, nil, nil, nil, nil)
	ctx, attempt, err := rl.BeginAccountQualityAttempt(context.Background(), &repo.child)
	require.NoError(t, err)
	defer attempt.Abort()
	owner, err := repo.GetByID(ctx, parentID)
	require.NoError(t, err)
	_, err = NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(ctx, owner, qualityRefreshExecutor{}, time.Minute)
	require.NoError(t, err)
	require.NoError(t, ValidateAccountQualityDispatch(ctx))
	require.True(t, rl.HandleAccountBalanceError(ctx, &repo.child, 403, []byte(`{"error":{"code":"insufficient_balance"}}`)))
	require.Equal(t, 1, parent.pauses)
	require.NotNil(t, parent.current.Extra[AccountBalancePauseKey])
}

type shadowQualityRefreshRepo struct {
	*qualityRefreshRepo
	child Account
}

func (r *shadowQualityRefreshRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if id == r.child.ID {
		child := r.child
		return &child, nil
	}
	return r.qualityRefreshRepo.GetByID(ctx, id)
}

// Exercise the same dispatch/result boundary as repository.doUpstreamRequest
// while retaining the adapter's real wallet-transition request construction.
type qualityGuardedUpstream struct {
	HTTPUpstream
	calls int
}

func (u *qualityGuardedUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if err := ValidateAccountQualityDispatch(req.Context()); err != nil {
		return nil, err
	}
	u.calls++
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}
	ObserveAccountQualityHTTPResult(req.Context(), resp, nil)
	return resp, nil
}

func TestQualityCreditsTransitionKeepsOneDedicatedPermit(t *testing.T) {
	rl := NewRateLimitService(nil, nil, nil, nil, nil)
	a := &Account{ID: 503, Platform: PlatformAntigravity, Extra: map[string]any{"allow_overages": true}}
	ctx, attempt, err := rl.BeginAccountQualityAttempt(context.Background(), a)
	require.NoError(t, err)
	defer attempt.Abort()
	ObserveAccountQualityHTTPResult(ctx, &http.Response{StatusCode: 429}, nil)
	require.NoError(t, ValidateAccountQualityDispatch(ctx), "provider adapters now own bounded 429 retry decisions")
	upstream := &qualityGuardedUpstream{}
	params := antigravityRetryLoopParams{ctx: ctx, account: a, accessToken: "test", action: "generateContent", body: []byte(`{"model":"model","request":{}}`), httpUpstream: upstream}
	svc := &AntigravityGatewayService{}
	body := []byte(`{"error":{"message":"QUOTA_EXHAUSTED"}}`)
	result := svc.attemptCreditsOveragesRetry(params, "https://antigravity.test", "model", 0, 429, body)
	require.NotNil(t, result.resp, "the free-to-paid wallet transition must reach upstream")
	require.NoError(t, result.resp.Body.Close())
	require.Equal(t, 1, upstream.calls)
	result = svc.attemptCreditsOveragesRetry(params, "https://antigravity.test", "model", 0, 429, body)
	require.Nil(t, result.resp, "even a successful transition cannot be reused")
	require.Equal(t, 1, upstream.calls)
}

func TestQualityCreditsTransitionCannotBypassFinancialFailure(t *testing.T) {
	rl := NewRateLimitService(nil, nil, nil, nil, nil)
	ctx, attempt, err := rl.BeginAccountQualityAttempt(context.Background(), &Account{ID: 504})
	require.NoError(t, err)
	defer attempt.Abort()
	ObserveAccountQualityHTTPResult(ctx, &http.Response{StatusCode: 429}, nil)
	attempt.balance.Store(true)
	err = ValidateAccountQualityDispatch(context.WithValue(ctx, accountQualityCreditsTransitionKey{}, true))
	require.Error(t, err)
}

func TestQualityObservationPreservesClassifiedFailureScope(t *testing.T) {
	for _, scope := range []string{"request_capacity", "request", "provider"} {
		t.Run(scope, func(t *testing.T) {
			rl := NewRateLimitService(nil, nil, nil, nil, nil)
			a := &Account{ID: 505, Platform: PlatformOpenAI}
			ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model"})
			for i := 0; i < 3; i++ {
				next, attempt, err := rl.BeginAccountQualityAttempt(ctx, a)
				require.NoError(t, err)
				body := []byte(`{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`)
				resp := &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(string(body)))}
				ObserveAccountQualityHTTPResult(next, resp, nil)
				_, err = io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				failure := &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScope(scope)}
				if scope == "request_capacity" {
					failure = newOpenAIUpstreamFailoverError(503, nil, body, "Our servers are currently overloaded. Please try again later.", false)
					require.True(t, failure.RequestScopedTransient)
				}
				require.Same(t, failure, attempt.Finish(nil, failure), "the safety extension must preserve the adapter retry scope")
			}
			next, attempt, err := rl.BeginAccountQualityAttempt(ctx, a)
			require.NoError(t, err, "request/provider errors must not add a second global breaker")
			defer attempt.Abort()
			require.NoError(t, ValidateAccountQualityDispatch(next))
		})
	}
}

func TestQualityV158PassthroughDebtPrecedesCommit(t *testing.T) {
	for _, failoverPath := range []bool{false, true} {
		repo := newQualityRefreshRepo()
		rl := NewRateLimitService(repo, nil, nil, nil, nil)
		svc := &OpenAIGatewayService{rateLimitService: rl}
		account, err := repo.GetByID(context.Background(), 501)
		require.NoError(t, err)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		body := []byte(`{"error":{"message":"Insufficient account balance"}}`)
		resp := &http.Response{StatusCode: 403, Header: http.Header{}}
		if failoverPath {
			err = svc.handleFailoverErrorResponsePassthroughV158(context.Background(), resp, c, account, nil, body)
		} else {
			err = svc.handleErrorResponsePassthroughV158(context.Background(), resp, c, account, nil, body)
		}
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.True(t, failure.ShouldRetryNextAccount())
		require.False(t, c.Writer.Written())
		require.Equal(t, 1, repo.pauses)
	}
}

func TestQualityForwardV158DebtStopsBeforePassthroughResponse(t *testing.T) {
	repo := newQualityRefreshRepo()
	rl := NewRateLimitService(repo, nil, nil, nil, nil)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Insufficient account balance"}}`)),
	}}
	svc := &OpenAIGatewayService{rateLimitService: rl, cfg: &config.Config{}, httpUpstream: upstream}
	account, err := repo.GetByID(context.Background(), 501)
	require.NoError(t, err)
	body := []byte(`{"model":"gpt-5.5","instructions":"quality regression","input":"test","stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body)))
	_, err = svc.Forward(c.Request.Context(), c, account, body)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.ShouldRetryNextAccount())
	require.NotNil(t, upstream.lastReq, "exercise the real forwarder entry")
	require.False(t, c.Writer.Written())
	require.Equal(t, 1, repo.pauses)
}

func TestQualityWSDispatchFailurePreservesSecondTurn(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "http_bridge"}[bridge], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			a := Account{ID: 506, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			if bridge {
				a.Platform = PlatformGrok
			}
			repo := &balanceSafetyRepo{current: a}
			rl := NewRateLimitService(repo, nil, cfg, nil, nil)
			capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			pool.setClientDialerForTest(&openAIWSSingleConnDialer{conn: capture})
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_first\",\"model\":\"gpt-5.1\"}}\n\n"))}}
			svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: rl, httpUpstream: upstream, cache: &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
			errCh := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					errCh <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_, first, err := conn.Read(r.Context())
				if err != nil {
					errCh <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				hooks := &OpenAIWSIngressHooks{BeforeRequest: func(turn int, _ []byte, _ string) error {
					if turn == 2 {
						repo.mu.Lock()
						repo.current.Extra = map[string]any{AccountSchedulingGenerationKey: int64(1)}
						repo.mu.Unlock()
					}
					return nil
				}}
				errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, &a, "test", first, hooks)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"FIRST"}`)))
			_, _, err = client.Read(ctx)
			require.NoError(t, err)
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"SECOND"}`)))
			select {
			case err = <-errCh:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			payload, current := OpenAIWSCurrentTurnRetryPayload(err)
			require.True(t, current)
			require.Contains(t, string(payload), "SECOND")
			require.NotContains(t, string(payload), "FIRST")
			require.Equal(t, "gpt-5.1", gjson.GetBytes(payload, "model").String())
		})
	}
}

func TestQualityWSUnsafeDispatchFailureNeverReplaysFirstTurn(t *testing.T) {
	cause := &UpstreamFailoverError{StatusCode: 503, Reason: "scheduling_state_changed"}
	for _, body := range []string{
		`{"model":"model","previous_response_id":"external","input":"SECOND"}`,
		`{"model":"model","input":[{"type":"function_call_output","call_id":"missing","output":"SECOND"}]}`,
	} {
		err := openAIWSDispatchFailureForTurn(cause, 2, []byte(body), "model", nil, false, "")
		require.True(t, errors.Is(err, cause))
		payload, current := OpenAIWSCurrentTurnRetryPayload(err)
		require.True(t, current)
		require.Empty(t, payload, "handler must close instead of falling back to the first turn")
	}
}

func TestQualityWSDispatchFailureRebuildsToolContextFromPriorTurn(t *testing.T) {
	cause := &UpstreamFailoverError{StatusCode: 503, Reason: "quality_circuit_open"}
	prior := []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call_previous","name":"lookup","arguments":"{}"}`)}
	body := []byte(`{"model":"mapped-model","previous_response_id":"resp_previous","input":[{"type":"function_call_output","call_id":"call_previous","output":"SECOND"}]}`)
	err := openAIWSDispatchFailureForTurn(cause, 2, body, "public-model", prior, true, "resp_previous")
	payload, current := OpenAIWSCurrentTurnRetryPayload(err)
	require.True(t, current)
	require.NotEmpty(t, payload)
	require.False(t, gjson.GetBytes(payload, "previous_response_id").Exists())
	require.Equal(t, "public-model", gjson.GetBytes(payload, "model").String())
	require.True(t, AnalyzeToolCallOutputContextCoverageBytes(payload).ContextCoversAllCallIDs)
}
