package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHandleFailoverExhausted_SuppressesLimitContinuationUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))

	h := &OpenAIGatewayHandler{}
	h.handleFailoverExhausted(ctx, &service.UpstreamFailoverError{
		StatusCode:          http.StatusTooManyRequests,
		ResponseBody:        []byte(`{"error":{"message":"private limited account error"}}`),
		SuppressClientError: true,
	}, false)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "private limited account error")
	require.NotContains(t, recorder.Body.String(), "rate limit exceeded")
}

func TestHandleCountTokensFailoverExhausted_PreservesUnsupportedForNormalAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	h := &OpenAIGatewayHandler{}
	h.handleCountTokensFailoverExhausted(ctx, &service.UpstreamFailoverError{
		StatusCode:       http.StatusNotFound,
		ClientStatusCode: http.StatusNotFound,
		ClientMessage:    "Token counting is not supported by upstream",
	})

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Token counting is not supported by upstream")
}

func TestHandleCountTokensFailoverExhausted_SuppressesOptedInAccountError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	h := &OpenAIGatewayHandler{}
	h.handleCountTokensFailoverExhausted(ctx, &service.UpstreamFailoverError{
		StatusCode:          http.StatusNotFound,
		ClientStatusCode:    http.StatusNotFound,
		ClientMessage:       "private unsupported detail",
		SuppressClientError: true,
	})

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "private unsupported detail")
}

func TestPrepareOpenAICountTokensFailover_OnlyOptedInAccountRetries(t *testing.T) {
	normalAccount := &service.Account{Platform: service.PlatformOpenAI, Extra: map[string]any{}}
	normalErr := &service.UpstreamFailoverError{NextAccountAction: service.NextAccountRetry}
	prepareOpenAICountTokensFailover(normalAccount, normalErr)
	require.False(t, normalErr.ShouldRetryNextAccount())
	require.False(t, normalErr.SuppressClientError)

	optedInAccount := &service.Account{
		Platform: service.PlatformOpenAI,
		Extra: map[string]any{
			service.OpenAIContinueSchedulingAfterLimitExtraKey: true,
		},
	}
	optedInErr := &service.UpstreamFailoverError{NextAccountAction: service.NextAccountStop, Scope: service.GatewayFailureScopeAccount}
	prepareOpenAICountTokensFailover(optedInAccount, optedInErr)
	require.True(t, optedInErr.ShouldRetryNextAccount())
	require.True(t, optedInErr.SuppressClientError)
}
