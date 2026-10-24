//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// stubSmartRetryCache 用于 handleSmartRetry 测试的 GatewayCache mock
// 仅关注 DeleteSessionAccountID 的调用记录
type stubSmartRetryCache struct {
	GatewayCache // 嵌入接口，未实现的方法 panic（确保只调用预期方法）
	deleteCalls  []deleteSessionCall
}

type deleteSessionCall struct {
	groupID     int64
	sessionHash string
}

func (c *stubSmartRetryCache) DeleteSessionAccountID(_ context.Context, groupID int64, sessionHash string) error {
	c.deleteCalls = append(c.deleteCalls, deleteSessionCall{groupID: groupID, sessionHash: sessionHash})
	return nil
}

// mockSmartRetryUpstream 用于 handleSmartRetry 测试的 mock upstream
type mockSmartRetryUpstream struct {
	responses      []*http.Response
	responseBodies [][]byte // 缓存的 response body 字节（用于 repeatLast 重建）
	errors         []error
	callIdx        int
	calls          []string
	requestBodies  [][]byte
	repeatLast     bool // 超出范围时重复最后一个响应
}

func (m *mockSmartRetryUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	idx := m.callIdx
	m.calls = append(m.calls, req.URL.String())
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		m.requestBodies = append(m.requestBodies, body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	} else {
		m.requestBodies = append(m.requestBodies, nil)
	}
	m.callIdx++

	// 确定使用哪个索引
	respIdx := idx
	if respIdx >= len(m.responses) {
		if !m.repeatLast || len(m.responses) == 0 {
			return nil, nil
		}
		respIdx = len(m.responses) - 1
	}

	resp := m.responses[respIdx]
	respErr := m.errors[respIdx]
	if resp == nil {
		return nil, respErr
	}

	// 首次调用时缓存 body 字节
	if respIdx >= len(m.responseBodies) {
		for len(m.responseBodies) <= respIdx {
			m.responseBodies = append(m.responseBodies, nil)
		}
	}
	if m.responseBodies[respIdx] == nil && resp.Body != nil {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		m.responseBodies[respIdx] = bodyBytes
	}

	// 用缓存的 body 重建 reader（支持重试场景多次读取）
	cloned := *resp
	if m.responseBodies[respIdx] != nil {
		cloned.Body = io.NopCloser(bytes.NewReader(m.responseBodies[respIdx]))
	}
	return &cloned, respErr
}

func (m *mockSmartRetryUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return m.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestAntigravityUpstreamFailureDoesNotReplay(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, single := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/single=%t", status, single), func(t *testing.T) {
				ctx := context.Background()
				if single {
					ctx = ctxWithSingleAccountRetry()
				}
				upstream := &mockSmartRetryUpstream{}
				a := &Account{ID: 91, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
				body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"RATE_LIMIT_EXCEEDED"}}`)
				params := antigravityRetryLoopParams{ctx: ctx, account: a, httpUpstream: upstream}
				result := (&AntigravityGatewayService{}).handleSmartRetry(params, &http.Response{StatusCode: status, Header: http.Header{}}, body, "https://unused.test", 0, []string{"https://unused.test"})
				require.Equal(t, smartRetryActionBreakWithResp, result.action)
				require.NotNil(t, result.resp)
				require.Equal(t, status, result.resp.StatusCode)
				require.Empty(t, upstream.requestBodies, "failed account must not receive another generation")
			})
		}
	}
}

func TestAntigravityCapacityUsesBoundedProviderRetry(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED","metadata":{"model":"gemini-3-pro-high"}}]}}`)
	failure := antigravityCapacityFailover(account, 503, nil, body)
	require.NotNil(t, failure)
	require.True(t, failure.RetryableOnSameAccount)
	require.True(t, failure.RequestScopedTransient)
	require.Equal(t, 3, failure.SameAccountRetryMax)
	require.Equal(t, GatewayFailureScopeProvider, failure.Scope)
	require.Nil(t, antigravityCapacityFailover(account, 429, nil, body), "quota errors must use their existing quota policy")
	require.Nil(t, antigravityCapacityFailover(account, 503, nil, []byte(`{"error":{"message":"Insufficient account balance"}}`)))
}
