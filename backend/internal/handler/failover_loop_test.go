package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock
// ---------------------------------------------------------------------------

// mockTempUnscheduler 记录 TempUnscheduleRetryableError 的调用信息。
type mockTempUnscheduler struct {
	calls []tempUnscheduleCall
}

type tempUnscheduleCall struct {
	accountID   int64
	failoverErr *service.UpstreamFailoverError
}

func (m *mockTempUnscheduler) TempUnscheduleRetryableError(_ context.Context, accountID int64, failoverErr *service.UpstreamFailoverError) {
	m.calls = append(m.calls, tempUnscheduleCall{accountID: accountID, failoverErr: failoverErr})
}

const maxSameAccountRetries = 3

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestFailoverErr(statusCode int, retryable, forceBilling bool) *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             statusCode,
		RetryableOnSameAccount: retryable,
		ForceCacheBilling:      forceBilling,
	}
}

// ---------------------------------------------------------------------------
// NewFailoverState 测试
// ---------------------------------------------------------------------------

func TestNewFailoverState(t *testing.T) {
	t.Run("初始化字段正确", func(t *testing.T) {
		fs := NewFailoverState(5, true)
		require.Equal(t, 5, fs.MaxSwitches)
		require.Equal(t, 0, fs.SwitchCount)
		require.NotNil(t, fs.FailedAccountIDs)
		require.Empty(t, fs.FailedAccountIDs)
		require.Nil(t, fs.LastFailoverErr)
		require.False(t, fs.ForceCacheBilling)
		require.True(t, fs.hasBoundSession)
	})

	t.Run("无绑定会话", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		require.Equal(t, 3, fs.MaxSwitches)
		require.False(t, fs.hasBoundSession)
	})

	t.Run("零最大切换次数", func(t *testing.T) {
		fs := NewFailoverState(0, false)
		require.Equal(t, 0, fs.MaxSwitches)
	})
}

// ---------------------------------------------------------------------------
// sleepWithContext 测试
// ---------------------------------------------------------------------------

func TestSleepWithContext(t *testing.T) {
	t.Run("零时长立即返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), 0)
		require.True(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("负时长立即返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), -1*time.Second)
		require.True(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("正常等待后返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), 50*time.Millisecond)
		elapsed := time.Since(start)
		require.True(t, ok)
		require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
		require.Less(t, elapsed, 500*time.Millisecond)
	})

	t.Run("已取消context立即返回false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		ok := sleepWithContext(ctx, 5*time.Second)
		require.False(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("等待期间context取消返回false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		ok := sleepWithContext(ctx, 5*time.Second)
		elapsed := time.Since(start)
		require.False(t, ok)
		require.Less(t, elapsed, 500*time.Millisecond)
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 基本切换流程
// ---------------------------------------------------------------------------

func TestHandleFailoverError_BasicSwitch(t *testing.T) {
	t.Run("显式停止不切换账号且旧错误默认仍切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		stopErr := &service.UpstreamFailoverError{
			Stage:             service.GatewayFailureStageAccountAuth,
			Scope:             service.GatewayFailureScopeProvider,
			NextAccountAction: service.NextAccountStop,
		}

		action := fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformGrok, maxSameAccountRetries, stopErr)

		require.Equal(t, FailoverExhausted, action)
		require.Zero(t, fs.SwitchCount)
		require.Empty(t, fs.FailedAccountIDs)
		require.Equal(t, stopErr, fs.LastFailoverErr)

		legacyErr := newTestFailoverErr(http.StatusTooManyRequests, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformGrok, maxSameAccountRetries, legacyErr)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
	})

	t.Run("已取消的认证失败不改变切换状态", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := &service.UpstreamFailoverError{
			Stage:             service.GatewayFailureStageAccountAuth,
			Scope:             service.GatewayFailureScopeAccount,
			NextAccountAction: service.NextAccountRetry,
		}

		action := fs.HandleFailoverError(ctx, mock, 101, service.PlatformGrok, maxSameAccountRetries, err)

		require.Equal(t, FailoverCanceled, action)
		require.Zero(t, fs.SwitchCount)
		require.Empty(t, fs.FailedAccountIDs)
		require.Nil(t, fs.LastFailoverErr)
		require.Empty(t, mock.calls)
	})

	t.Run("非重试错误_非Antigravity_直接切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
		require.Equal(t, err, fs.LastFailoverErr)
		require.False(t, fs.ForceCacheBilling)
		require.Empty(t, mock.calls, "不应调用 TempUnschedule")
	})

	t.Run("非重试错误_Antigravity_第一次切换无延迟", func(t *testing.T) {
		// switchCount 从 0→1 时，sleepFailoverDelay(ctx, 1) 的延时 = (1-1)*1s = 0
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false)

		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformAntigravity, maxSameAccountRetries, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Less(t, elapsed, 200*time.Millisecond, "第一次切换延迟应为 0")
	})

	t.Run("非重试错误_Antigravity_第二次切换有1秒延迟", func(t *testing.T) {
		// switchCount 从 1→2 时，sleepFailoverDelay(ctx, 2) 的延时 = (2-1)*1s = 1s
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		fs.SwitchCount = 1 // 模拟已切换一次

		err := newTestFailoverErr(500, false, false)
		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 200, service.PlatformAntigravity, maxSameAccountRetries, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 2, fs.SwitchCount)
		require.Less(t, elapsed, 200*time.Millisecond, "有其他候选时不应等待故障账号")
		require.Less(t, elapsed, 3*time.Second)
	})

	t.Run("连续切换直到耗尽", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(2, false)

		// 第一次切换：0→1
		err1 := newTestFailoverErr(500, false, false)
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err1)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)

		// 第二次切换：1→2
		err2 := newTestFailoverErr(502, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", maxSameAccountRetries, err2)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 2, fs.SwitchCount)

		// 第三次已耗尽：SwitchCount(2) >= MaxSwitches(2)
		err3 := newTestFailoverErr(503, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 300, "openai", maxSameAccountRetries, err3)
		require.Equal(t, FailoverExhausted, action)
		require.Equal(t, 2, fs.SwitchCount, "耗尽时不应继续递增")

		// 验证失败账号列表
		require.Len(t, fs.FailedAccountIDs, 3)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
		require.Contains(t, fs.FailedAccountIDs, int64(200))
		require.Contains(t, fs.FailedAccountIDs, int64(300))

		// LastFailoverErr 应为最后一次的错误
		require.Equal(t, err3, fs.LastFailoverErr)
	})

	t.Run("MaxSwitches为0时首次即耗尽", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(0, false)
		err := newTestFailoverErr(500, false, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)
		require.Equal(t, FailoverExhausted, action)
		require.Equal(t, 0, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 缓存计费 (ForceCacheBilling)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — 同账号重试 (RetryableOnSameAccount)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — TempUnschedule 调用验证
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — Context 取消
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — FailedAccountIDs 跟踪
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — LastFailoverErr 更新
// ---------------------------------------------------------------------------

func TestHandleFailoverError_LastFailoverErr(t *testing.T) {
	t.Run("每次调用都更新LastFailoverErr", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		err1 := newTestFailoverErr(500, false, false)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err1)
		require.Equal(t, err1, fs.LastFailoverErr)

		err2 := newTestFailoverErr(502, false, false)
		fs.HandleFailoverError(context.Background(), mock, 200, "openai", maxSameAccountRetries, err2)
		require.Equal(t, err2, fs.LastFailoverErr)
	})

	t.Run("同账号重试时也更新LastFailoverErr", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		err := newTestFailoverErr(400, true, false)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)
		require.Equal(t, err, fs.LastFailoverErr)
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 综合集成场景
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleFailoverError — 边界条件
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// HandleSelectionExhausted 测试
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// failoverClientGone 测试
// ---------------------------------------------------------------------------

func TestFailoverClientGone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("活跃请求返回false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		require.False(t, failoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "不应改动状态码")
	})

	t.Run("客户端已断开_返回true并标记499", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

		require.True(t, failoverClientGone(c))
		require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	})

	t.Run("响应已提交_不改状态码", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		c.String(http.StatusOK, "partial")

		require.True(t, failoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "已提交的状态码不应被覆盖")
	})

	t.Run("nil安全", func(t *testing.T) {
		require.False(t, failoverClientGone(nil))
	})
}

func TestFailoverNeverRevisitsExhaustedAccountAcrossPlatforms(t *testing.T) {
	for _, platform := range []string{"openai", "anthropic", "gemini", "antigravity", "deepseek", "grok"} {
		for _, status := range []int{401, 403, 429, 500, 502, 503, 529} {
			t.Run(fmt.Sprintf("%s/%d", platform, status), func(t *testing.T) {
				fs := NewFailoverState(2, true)
				for i, id := range []int64{11, 22, 33} {
					action := fs.HandleFailoverError(context.Background(), &mockTempUnscheduler{}, id, platform, 0, newTestFailoverErr(status, true, false))
					if i < 2 {
						require.Equal(t, FailoverContinue, action)
					} else {
						require.Equal(t, FailoverExhausted, action)
					}
					require.Contains(t, fs.FailedAccountIDs, id)
					require.True(t, fs.ForceCacheBilling)
				}
				require.Len(t, fs.FailedAccountIDs, 3)
				require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
				require.Len(t, fs.FailedAccountIDs, 3, "exhaustion must never clear failed candidates")
			})
		}
	}
}
func TestFailoverCanceledDoesNotSelectAnotherAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fs := NewFailoverState(10, false)
	require.Equal(t, FailoverCanceled, fs.HandleFailoverError(ctx, &mockTempUnscheduler{}, 1, "openai", 3, newTestFailoverErr(503, true, false)))
	require.Zero(t, fs.SwitchCount)
	require.Empty(t, fs.FailedAccountIDs)
}
