package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func TestAccountTestServiceOpenAICompactAgentIdentityUsesFreshAssertion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, privateKey := newTestAgentIdentityKey(t)
	account := Account{
		ID:          21,
		Name:        "agent-identity",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":                  OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":           key.runtimeID,
			"agent_private_key":          privateKey,
			"task_id":                    key.taskID,
			"chatgpt_account_id":         "account-agent-test",
			"chatgpt_account_is_fedramp": true,
		},
	}
	repo := &snapshotUpdateAccountRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"compact-agent","status":"completed"}`)),
	}}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/21/test", bytes.NewReader(nil))

	require.NoError(t, svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact))
	require.Equal(t, "AgentAssertion", strings.SplitN(upstream.lastReq.Header.Get("Authorization"), " ", 2)[0])
	require.Equal(t, "account-agent-test", upstream.lastReq.Header.Get("chatgpt-account-id"))
	require.Equal(t, "true", upstream.lastReq.Header.Get("x-openai-fedramp"))
	require.NotContains(t, upstream.lastReq.Header.Get("Authorization"), privateKey)
}

func TestAccountTestServiceOpenAICompactAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:          22,
		Name:        "agent-identity-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   key.runtimeID,
			"agent_private_key":  privateKey,
			"task_id":            "task-compact-old",
			"chatgpt_account_id": "account-agent-compact-recovery",
		},
	}
	repo := &accountTestAgentIdentityRepo{account: account}
	registerCalls := 0
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registerCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-compact-new"}`)
	}))
	defer registerServer.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"compact-agent","status":"completed"}`))},
	}}
	invalidator := &agentIdentityWSInvalidationRecorder{}
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, agentIdentityWS: invalidator}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/22/test", bytes.NewReader(nil))

	require.NoError(t, svc.TestAccountConnection(c, account.ID, "gpt-5.4", "", AccountTestModeCompact))
	require.Equal(t, 1, registerCalls)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "task-compact-new", account.GetCredential("task_id"))
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, []int64{account.ID}, invalidator.accountIDs)
}

func TestOpenAIAgentIdentityPassthroughKeepsSessionAndPromptCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:       24,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   key.runtimeID,
			"agent_private_key":  privateKey,
			"task_id":            key.taskID,
			"chatgpt_account_id": "account-agent-passthrough",
		},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":true,"prompt_cache_key":"cache-agent"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("session_id", "client-session")
	c.Request.Header.Set("conversation_id", "client-conversation")
	c.Request.Header.Set("Authorization", "Bearer inbound-must-not-forward")

	svc := &OpenAIGatewayService{}
	req, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "")
	require.NoError(t, err)
	require.Equal(t, "AgentAssertion", strings.SplitN(req.Header.Get("Authorization"), " ", 2)[0])
	require.Equal(t, "account-agent-passthrough", req.Header.Get("chatgpt-account-id"))
	require.NotEqual(t, "client-session", req.Header.Get("session_id"))
	require.NotEqual(t, "client-conversation", req.Header.Get("conversation_id"))
	require.Equal(t, isolateOpenAISessionID(0, "client-session"), req.Header.Get("session_id"))
	require.Equal(t, isolateOpenAISessionID(0, "client-conversation"), req.Header.Get("conversation_id"))
	requestBody, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Contains(t, string(requestBody), `"prompt_cache_key":"cache-agent"`)

	// Authentication mode must not affect session isolation or prompt-cache
	// behavior. Compare the same request with the existing OAuth path instead
	// of pinning this test to an implementation-specific hash.
	oauthAccount := &Account{
		ID:       26,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-oauth-passthrough",
		},
	}
	oauthRecorder := httptest.NewRecorder()
	oauthContext, _ := gin.CreateTestContext(oauthRecorder)
	oauthContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	oauthContext.Request.Header.Set("session_id", "client-session")
	oauthContext.Request.Header.Set("conversation_id", "client-conversation")
	oauthReq, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), oauthContext, oauthAccount, body, "oauth-token")
	require.NoError(t, err)
	require.Equal(t, oauthReq.Header.Get("session_id"), req.Header.Get("session_id"))
	require.Equal(t, oauthReq.Header.Get("conversation_id"), req.Header.Get("conversation_id"))
}

func TestOpenAIAgentIdentityErrorRedactionDoesNotLeakCredentialValues(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:       25,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  key.runtimeID,
			"agent_private_key": privateKey,
			"task_id":           key.taskID,
			"access_token":      key.runtimeID + "-oauth-value",
		},
	}
	svc := &OpenAIGatewayService{}
	oauthValue := account.GetCredential("access_token")
	redacted := svc.redactAgentIdentitySensitiveBody(context.Background(), account, []byte(`{"message":"runtime-test task-test `+oauthValue+` AgentAssertion abc123"}`))
	require.NotContains(t, string(redacted), key.runtimeID)
	require.NotContains(t, string(redacted), key.taskID)
	require.NotContains(t, string(redacted), oauthValue)
	require.NotContains(t, string(redacted), "AgentAssertion abc123")
	require.Contains(t, string(redacted), "[redacted]")
}

func TestReadOpenAIUpstreamErrorRedactsBeforeMessageExtraction(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":        OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-secret",
			"task_id":          "task-secret",
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"runtime-secret task-secret AgentAssertion assertion-secret"}}`,
		)),
	}
	svc := &OpenAIGatewayService{}

	body, message := svc.readOpenAIUpstreamError(context.Background(), account, resp)
	require.NotContains(t, string(body), "runtime-secret")
	require.NotContains(t, string(body), "task-secret")
	require.NotContains(t, string(body), "assertion-secret")
	require.NotContains(t, message, "runtime-secret")
	require.Contains(t, message, "[redacted]")
	rewound, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, body, rewound)
}

func TestAgentIdentityStreamingErrorsAreRedactedBeforeProtocolConversion(t *testing.T) {
	account := &Account{
		ID:       27,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":        OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-stream-secret",
			"task_id":          "task-stream-secret",
		},
	}
	payload := `data: {"type":"response.failed","error":{"code":"content_policy","message":"runtime-stream-secret task-stream-secret AgentAssertion assertion-stream-secret"}}` + "\n\n"

	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *http.Response, *gin.Context) error
	}{
		{
			name: "chat completions",
			run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
				_, err := svc.handleChatStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
				return err
			},
		},
		{
			name: "anthropic messages",
			run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
				_, err := svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(payload)),
			}
			err := tt.run(&OpenAIGatewayService{cfg: &config.Config{}}, resp, c)
			require.Error(t, err)
			combined := err.Error() + recorder.Body.String()
			require.NotContains(t, combined, "runtime-stream-secret")
			require.NotContains(t, combined, "task-stream-secret")
			require.NotContains(t, combined, "assertion-stream-secret")
			require.Contains(t, combined, "[redacted]")
		})
	}
}

func TestAgentIdentityStreamingReadErrorsAreRedacted(t *testing.T) {
	account := &Account{
		ID: 28, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": "runtime-read-secret",
			"task_id": "task-read-secret",
		},
	}
	readErr := errors.New("proxy read failed runtime-read-secret task-read-secret AgentAssertion assertion-read-secret")
	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *http.Response, *gin.Context) error
	}{
		{name: "chat", run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
			_, err := svc.handleChatStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
			return err
		}},
		{name: "messages", run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) error {
			_, err := svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/test", nil)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: errReadCloser{err: readErr}}
			err := tt.run(&OpenAIGatewayService{cfg: &config.Config{}}, resp, c)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "runtime-read-secret")
			require.NotContains(t, err.Error(), "task-read-secret")
			require.NotContains(t, err.Error(), "assertion-read-secret")
			require.Contains(t, err.Error(), "[redacted]")
		})
	}
}

func TestOpenAIAuthenticationHeadersPreserveOAuthPATAndAPIKeyBearerModes(t *testing.T) {
	svc := &OpenAIGatewayService{}
	tests := []struct {
		name    string
		account *Account
		token   string
	}{
		{name: "oauth", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, token: "oauth-runtime-token"},
		{name: "personal access token", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}}, token: "pat-runtime-token"},
		{name: "api key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, token: "api-key-runtime-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers, err := svc.buildOpenAIAuthenticationHeaders(context.Background(), tt.account, tt.token)
			require.NoError(t, err)
			require.Equal(t, "Bearer "+tt.token, headers.Get("Authorization"))
		})
	}
}

func TestOpenAIWSAgentIdentityRecoveryRequiresTaskInvalidBody(t *testing.T) {
	require.False(t, isAgentIdentityTaskInvalidWSDialError(&openAIWSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"code":"invalid_signature"}}`),
	}))
	require.True(t, isAgentIdentityTaskInvalidWSDialError(&openAIWSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"code":"invalid_task_id"}}`),
	}))
	passthroughDialErr := normalizeOpenAIWSPassthroughDialError(
		&openAIWSHandshakeError{
			Body: []byte(`{"error":{"code":"invalid_task_id"}}`),
			Err:  errors.New("handshake rejected"),
		},
		http.StatusUnauthorized,
		http.Header{"X-Request-Id": []string{"req-invalid-task"}},
	)
	require.True(t, isAgentIdentityTaskInvalidWSDialError(passthroughDialErr))
	require.Equal(t, "req-invalid-task", passthroughDialErr.ResponseHeaders.Get("X-Request-Id"))
}

func TestValidateOpenAIWSBearerTokenAllowsAgentIdentityWithoutStoredToken(t *testing.T) {
	t.Run("Given Agent Identity When a WS path receives no bearer token Then dial-time assertion auth is allowed", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"auth_mode": OpenAIAuthModeAgentIdentity,
			},
		}

		require.NoError(t, validateOpenAIWSBearerToken(account, ""))
	})

	t.Run("Given bearer credentials When a WS path receives no token Then the request is rejected", func(t *testing.T) {
		accounts := []*Account{
			{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}},
			{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		}

		for _, account := range accounts {
			require.EqualError(t, validateOpenAIWSBearerToken(account, ""), "token is empty")
		}
	})
}

func TestValidateOpenAIWSAccountTokenResolvesAgentIdentityShadow(t *testing.T) {
	parentID := int64(81)
	parent := Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode": OpenAIAuthModeAgentIdentity,
		},
	}
	shadow := &Account{
		ID:              82,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		ParentAccountID: &parentID,
	}
	svc := &OpenAIGatewayService{accountRepo: &snapshotUpdateAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{parent}},
	}}

	require.NoError(t, svc.validateOpenAIWSAccountToken(context.Background(), shadow, ""))
}

func TestRecoverAgentIdentityTaskUsesShadowRequestTaskSnapshot(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	parentID := int64(831)
	parent := &Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  key.runtimeID,
			"agent_private_key": privateKey,
			"task_id":           "task-shadow-old",
		},
	}
	shadow := &Account{
		ID:              832,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		ParentAccountID: &parentID,
	}
	repo := &countingAgentIdentityAccountRepo{account: parent}
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"task_id":"task-shadow-new"}`)
	}))
	defer registerServer.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	svc := &OpenAIGatewayService{accountRepo: repo}
	require.NoError(t, svc.recoverAgentIdentityTask(context.Background(), shadow, "task-shadow-old"))
	require.Equal(t, "task-shadow-new", parent.GetCredential("task_id"))
}

func TestRecoverAgentIdentityTaskDoesNotReplaceConcurrentlyRefreshedShadowTask(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	parentID := int64(833)
	parent := &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID,
			"agent_private_key": privateKey, "task_id": "task-old",
		},
	}
	shadow := &Account{ID: 834, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &countingAgentIdentityAccountRepo{account: parent}
	svc := &OpenAIGatewayService{accountRepo: repo}
	metadata, err := svc.agentIdentityRequestMetadata(context.Background(), shadow)
	require.NoError(t, err)
	require.Equal(t, "task-old", metadata.taskIDUsed)

	parent.Credentials["task_id"] = "task-concurrently-refreshed"
	require.NoError(t, svc.recoverAgentIdentityTask(context.Background(), shadow, metadata.taskIDUsed))
	require.Equal(t, "task-concurrently-refreshed", parent.GetCredential("task_id"))
}

func TestAgentIdentityTaskIDFromAuthorization(t *testing.T) {
	key, _ := newTestAgentIdentityKey(t)
	key.taskID = "task-used-on-wire"
	assertion, err := buildAgentAssertion(key, time.Now())
	require.NoError(t, err)
	require.Equal(t, key.taskID, agentIdentityTaskIDFromAuthorization(assertion))
	require.Empty(t, agentIdentityTaskIDFromAuthorization("Bearer token"))
}

func TestAgentIdentitySensitiveBodyRedactorResolvesShadowOncePerRequest(t *testing.T) {
	parentID := int64(841)
	parent := &Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":        OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-shadow-secret",
			"task_id":          "task-shadow-secret",
		},
	}
	shadow := &Account{ID: 842, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &countingAgentIdentityAccountRepo{account: parent}
	svc := &OpenAIGatewayService{accountRepo: repo}

	redact, err := svc.agentIdentitySensitiveBodyRedactor(context.Background(), shadow)
	require.NoError(t, err)
	for range 100 {
		body := redact([]byte(`{"error":{"message":"runtime-shadow-secret task-shadow-secret"}}`))
		require.NotContains(t, string(body), "runtime-shadow-secret")
		require.NotContains(t, string(body), "task-shadow-secret")
	}
	require.Equal(t, 1, repo.getByIDCalls)
}

func TestAgentIdentitySensitiveBodyRedactorFailsClosedWhenShadowResolutionFails(t *testing.T) {
	parentID := int64(851)
	shadow := &Account{ID: 852, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &countingAgentIdentityAccountRepo{getByIDErr: errors.New("repository unavailable")}
	svc := &OpenAIGatewayService{accountRepo: repo}

	redact, err := svc.agentIdentitySensitiveBodyRedactor(context.Background(), shadow)
	require.Error(t, err)
	require.Nil(t, redact)
	require.Equal(t, 1, repo.getByIDCalls)
}

func TestRedactAgentIdentitySensitiveBodyFailsClosedWhenShadowResolutionFails(t *testing.T) {
	parentID := int64(861)
	shadow := &Account{ID: 862, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &countingAgentIdentityAccountRepo{getByIDErr: errors.New("repository unavailable")}
	svc := &OpenAIGatewayService{accountRepo: repo}
	body := []byte(`{"error":{"message":"runtime-secret task-secret AgentAssertion assertion-secret"}}`)

	redacted := svc.redactAgentIdentitySensitiveBody(context.Background(), shadow, body)
	require.JSONEq(t, `{"error":{"type":"upstream_error","message":"[redacted]"}}`, string(redacted))
	require.NotContains(t, string(redacted), "runtime-secret")
	require.NotContains(t, string(redacted), "task-secret")
	require.NotContains(t, string(redacted), "assertion-secret")
	require.Equal(t, 1, repo.getByIDCalls)
}

func TestRedactAgentIdentitySensitiveBodyPreservesTerminalEventTypeWhenFailingClosed(t *testing.T) {
	parentID := int64(863)
	shadow := &Account{ID: 864, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	svc := &OpenAIGatewayService{accountRepo: &countingAgentIdentityAccountRepo{getByIDErr: errors.New("repository unavailable")}}

	redacted := svc.redactAgentIdentitySensitiveBody(context.Background(), shadow, []byte(`{"type":"response.failed","response":{"error":{"message":"task-secret"}}}`))
	require.Equal(t, "response.failed", gjson.GetBytes(redacted, "type").String())
	require.Equal(t, "failed", gjson.GetBytes(redacted, "response.status").String())
	require.NotContains(t, string(redacted), "task-secret")
}

func TestAgentIdentitySensitiveErrorRedactsWebSocketCloseReason(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":        OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-close-secret",
			"task_id":          "task-close-secret",
		},
	}
	svc := &OpenAIGatewayService{}
	redact, err := svc.agentIdentitySensitiveBodyRedactor(context.Background(), account)
	require.NoError(t, err)
	rawErr := coderws.CloseError{
		Code:   coderws.StatusPolicyViolation,
		Reason: "runtime-close-secret task-close-secret AgentAssertion assertion-close-secret",
	}

	safeErr := redactAgentIdentitySensitiveError(redact, rawErr)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(rawErr), "classification must use the original error")
	require.NotContains(t, safeErr.Error(), "runtime-close-secret")
	require.NotContains(t, safeErr.Error(), "task-close-secret")
	require.NotContains(t, safeErr.Error(), "assertion-close-secret")
	require.Contains(t, safeErr.Error(), "[redacted]")
}

func TestAgentIdentitySensitiveErrorDoesNotExposeRawCause(t *testing.T) {
	redactor := func(body []byte) []byte { return bytes.ReplaceAll(body, []byte("task-secret"), []byte("[redacted]")) }
	rawErr := &agentIdentitySecretTestError{secret: "task-secret", cause: context.DeadlineExceeded}
	safeErr := redactAgentIdentitySensitiveError(redactor, rawErr)

	require.ErrorIs(t, rawErr, context.DeadlineExceeded, "classification must happen before redaction")
	require.NotErrorIs(t, safeErr, context.DeadlineExceeded)
	require.Nil(t, errors.Unwrap(safeErr))
	var recovered *agentIdentitySecretTestError
	require.False(t, errors.As(safeErr, &recovered))
	require.NotContains(t, safeErr.Error(), "task-secret")
	require.Contains(t, safeErr.Error(), "[redacted]")
}

type agentIdentitySecretTestError struct {
	secret string
	cause  error
}

func (e *agentIdentitySecretTestError) Error() string { return e.secret + ": " + e.cause.Error() }
func (e *agentIdentitySecretTestError) Unwrap() error { return e.cause }

func TestAgentIdentityRequestRedactorTracksTaskIDUsedOnWire(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":        OpenAIAuthModeAgentIdentity,
			"agent_runtime_id": "runtime-secret",
			"task_id":          "task-snapshot",
		},
	}
	svc := &OpenAIGatewayService{}
	metadata, err := svc.agentIdentityRequestMetadata(context.Background(), account)
	require.NoError(t, err)
	metadata.bindTaskID("task-used-on-wire")

	ctx := withAgentIdentityRequestRedactor(context.Background(), metadata.redactor)
	redact, err := svc.agentIdentitySensitiveBodyRedactor(ctx, account)
	require.NoError(t, err)
	redacted := string(redact([]byte("task-snapshot task-used-on-wire runtime-secret")))
	require.NotContains(t, redacted, "task-snapshot")
	require.NotContains(t, redacted, "task-used-on-wire")
	require.NotContains(t, redacted, "runtime-secret")
}

func TestAgentIdentityRequestRedactorBindsFullAndBareAssertion(t *testing.T) {
	key, _ := newTestAgentIdentityKey(t)
	assertion, err := buildAgentAssertion(key, time.Now())
	require.NoError(t, err)
	state := newAgentIdentityRedactionState(nil)
	metadata := agentIdentityRequestMetadata{redactionState: state, redactor: state.redact}
	metadata.bindAuthorization(assertion)

	redacted := string(metadata.redactor([]byte(assertion + " " + strings.TrimPrefix(assertion, "AgentAssertion "))))
	require.NotContains(t, redacted, strings.TrimPrefix(assertion, "AgentAssertion "))
	require.NotContains(t, redacted, assertion)
}

func TestAgentIdentitySensitiveDialErrorDropsRawBodyAndCause(t *testing.T) {
	redactor := agentIdentityBodyRedactor(func(body []byte) []byte {
		return bytes.ReplaceAll(body, []byte("dial-secret"), []byte("[redacted]"))
	})
	raw := &openAIWSDialError{
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"message":"dial-secret"}}`),
		Err:          errors.New("dial-secret"),
	}
	safe := redactAgentIdentitySensitiveDialError(redactor, raw)
	var safeDial *openAIWSDialError
	require.ErrorAs(t, safe, &safeDial)
	require.NotContains(t, string(safeDial.ResponseBody), "dial-secret")
	require.NotContains(t, safe.Error(), "dial-secret")
	require.Nil(t, errors.Unwrap(safeDial.Err))
}

func TestOpenAIWSPassthroughDialErrorClassifiesRawErrorAndReturnsSafeCause(t *testing.T) {
	svc := &OpenAIGatewayService{}
	rawErr := fmt.Errorf("runtime-dial-secret: %w", context.DeadlineExceeded)
	safeErr := errors.New("[redacted]: context deadline exceeded")

	mapped := svc.mapOpenAIWSPassthroughDialErrorWithSafeCause(rawErr, safeErr, 0, nil)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, mapped, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.statusCode)
	require.NotContains(t, mapped.Error(), "runtime-dial-secret")
	require.Contains(t, mapped.Error(), "[redacted]")
}

func TestOpenAIWSConnPoolHeadersFactoryRunsAtDialAndStalePrewarmIsDiscarded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSFakeDialer{})

	accountID := int64(22)
	ap := pool.getOrCreateAccountPool(accountID)
	factoryCalls := 0
	latestHeader := ""
	req := openAIWSAcquireRequest{
		Account: &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		WSURL:   "wss://example.com/v1/responses",
		HeadersFactory: func(_ context.Context, headers http.Header) (http.Header, error) {
			factoryCalls++
			latestHeader = "AgentAssertion dial-" + string(rune('0'+factoryCalls))
			if headers == nil {
				headers = make(http.Header)
			}
			headers.Set("Authorization", latestHeader)
			return headers, nil
		},
	}
	ap.mu.Lock()
	ap.lastAcquire = &req
	generation := ap.generation
	ap.mu.Unlock()

	pool.prewarmConns(accountID, req, 1, generation)
	require.Equal(t, 1, factoryCalls, "prewarm must generate authorization inside the actual dial")
	require.Equal(t, "AgentAssertion dial-1", latestHeader)

	pool.ClearAccount(accountID)
	ap.mu.Lock()
	require.Empty(t, ap.conns, "credential recovery must remove pooled connections")
	require.Nil(t, ap.lastAcquire, "credential recovery must discard delayed acquire state")
	require.Equal(t, generation+1, ap.generation)
	ap.mu.Unlock()

	// A prewarm captured before ClearAccount must not be admitted after recovery.
	pool.prewarmConns(accountID, req, 1, generation)
	ap.mu.Lock()
	require.Empty(t, ap.conns)
	ap.mu.Unlock()
}

func TestOpenAIAgentIdentityTaskInvalidRetriesExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:          23,
		Name:        "agent-identity",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":          OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":   key.runtimeID,
			"agent_private_key":  privateKey,
			"task_id":            "task-old",
			"chatgpt_account_id": "account-agent-retry",
		},
	}
	repo := &agentIdentityForwardRepo{account: account}
	registerCalls := 0
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registerCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-new"}`)
	}))
	defer registerServer.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	successBody := `{"id":"resp-agent-retry","object":"response","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(successBody))},
	}}
	require.True(t, isAgentIdentityTaskInvalidHTTPResponse(http.StatusUnauthorized, []byte(`{"error":{"code":"invalid_task_id"}}`)))
	svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))

	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))
	require.NoError(t, err)
	require.Equal(t, 1, registerCalls)
	require.Len(t, upstream.requests, 2)
	require.NotEqual(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "task-new", decodeAgentAssertionTask(t, upstream.requests[1].Header.Get("Authorization")))

	// Two consecutive invalid responses still produce only one retry for this
	// request; the recovery path must not loop indefinitely.
	upstream.responses = []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
	}
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))
	_, err = svc.Forward(context.Background(), c2, account, []byte(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))
	require.Error(t, err)
	require.Equal(t, 2, registerCalls)
	require.Len(t, upstream.requests, 4)

	// Passthrough uses the same one-shot task recovery contract.
	account.Extra = map[string]any{"openai_passthrough": true}
	account.Credentials["task_id"] = "task-old-passthrough"
	upstream.responses = []*http.Response{
		{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n"))},
	}
	rec3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(rec3)
	c3.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))
	_, err = svc.Forward(context.Background(), c3, account, []byte(`{"model":"gpt-5.4","instructions":"Reply OK","input":[],"stream":false}`))
	require.NoError(t, err)
	require.Equal(t, 3, registerCalls)
	require.Len(t, upstream.requests, 6)
}

func TestOpenAIAgentIdentityCompatRoutesRecoverInvalidTaskOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		path string
		body []byte
		call func(*OpenAIGatewayService, context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error)
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hi"}]}`),
			call: func(s *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.ForwardAsChatCompletions(ctx, c, account, body, "", "gpt-5.4")
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			body: []byte(`{"model":"gpt-5.4","stream":false,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`),
			call: func(s *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.ForwardAsAnthropic(ctx, c, account, body, "", "gpt-5.4")
			},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := &Account{
				ID:          int64(40 + index),
				Name:        "agent-identity-compat",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{
					"auth_mode":          OpenAIAuthModeAgentIdentity,
					"agent_runtime_id":   key.runtimeID,
					"agent_private_key":  privateKey,
					"task_id":            "task-compat-old",
					"chatgpt_account_id": "account-compat-recovery",
				},
			}
			repo := &agentIdentityForwardRepo{account: account}
			registerCalls := 0
			registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registerCalls++
				_, _ = io.WriteString(w, `{"task_id":"task-compat-new"}`)
			}))
			defer registerServer.Close()
			oldBase := openAIAgentIdentityAuthAPIBaseURL
			openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
			t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
				{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_task_id"}}`))},
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, httpUpstream: upstream}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))

			_, err := tt.call(svc, context.Background(), c, account, tt.body)
			require.Error(t, err)
			require.Equal(t, 1, registerCalls)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "task-compat-new", account.GetCredential("task_id"))
		})
	}
}

func decodeAgentAssertionTask(t *testing.T, header string) string {
	t.Helper()
	encoded := strings.TrimPrefix(header, "AgentAssertion ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal(decoded, &envelope))
	return envelope.TaskID
}

type agentIdentityForwardRepo struct {
	AccountRepository
	account *Account
}

type countingAgentIdentityAccountRepo struct {
	AccountRepository
	account      *Account
	getByIDCalls int
	getByIDErr   error
}

func (r *countingAgentIdentityAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.getByIDCalls++
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.account, nil
}

func (r *countingAgentIdentityAccountRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.account.Credentials = credentials
	return nil
}

type agentIdentityWSInvalidationRecorder struct {
	accountIDs []int64
}

func (r *agentIdentityWSInvalidationRecorder) InvalidateAgentIdentityWSConnections(accountID int64) {
	r.accountIDs = append(r.accountIDs, accountID)
}

type accountTestAgentIdentityRepo struct {
	AccountRepository
	account       *Account
	setErrorCalls int
}

func (r *accountTestAgentIdentityRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *accountTestAgentIdentityRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.account.Credentials = credentials
	return nil
}

func (r *accountTestAgentIdentityRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	return nil
}

func (r *accountTestAgentIdentityRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

func (r *agentIdentityForwardRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *agentIdentityForwardRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.account.Credentials = credentials
	return nil
}
