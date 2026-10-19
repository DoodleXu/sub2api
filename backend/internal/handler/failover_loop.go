package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// TempUnscheduler 用于 HandleFailoverError 中同账号重试耗尽后的临时封禁。
// GatewayService 隐式实现此接口。
type TempUnscheduler interface {
	TempUnscheduleRetryableError(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError)
}

// FailoverAction 表示 failover 错误处理后的下一步动作
type FailoverAction int

const (
	// FailoverContinue 继续循环（同账号重试或切换账号，调用方统一 continue）
	FailoverContinue FailoverAction = iota
	// FailoverExhausted 切换次数耗尽（调用方应返回错误响应）
	FailoverExhausted
	// FailoverCanceled context 已取消（调用方应直接 return）
	FailoverCanceled
)

const (
	// maxProfitVetoAttempts 单次请求内允许的分组利润门终检否决次数上限。
	// 利润否决不产生上游请求，因此不会推进 SwitchCount；没有独立上限的话，
	// 「选号 → 终检否决 → 重选」在候选池与账号快照短暂不一致时可以空转很久。
	// 取值与 maxAccountSwitches 默认值一致：混合定价的大分组仍有充分重选机会，
	// 同时把整池越线时的无谓选号开销限制在常数级。
	maxProfitVetoAttempts = 10
)

// profitVetoExhaustedMessage 是利润否决次数耗尽时返回给客户端的文案。
// 语义上等同于「无可用账号」：候选账号都不满足分组的利润约束。
const profitVetoExhaustedMessage = "No available accounts: all candidates rejected by group profit control"

// FailoverState 跨循环迭代共享的 failover 状态
type FailoverState struct {
	SwitchCount       int
	MaxSwitches       int
	FailedAccountIDs  map[int64]struct{}
	LastFailoverErr   *service.UpstreamFailoverError
	ForceCacheBilling bool
	hasBoundSession   bool

	// profitVetoedAccountIDs 记录被分组利润门终检否决的账号，是 FailedAccountIDs
	// 的子集。之所以单独维护：HandleSelectionExhausted 的 503 退避分支会清空
	// FailedAccountIDs，而利润否决在同一请求内的判定不会改变（下游倍率 D 已在
	// 请求开始冻结），被清空的账号会被立即重选并再次否决，形成没有任何上游请求、
	// SwitchCount 也不前进的活锁。清空后必须把它们放回排除集。
	profitVetoedAccountIDs map[int64]struct{}
	// profitVetoCount 本次请求累计的利润否决次数，用于 maxProfitVetoAttempts 上限。
	profitVetoCount int
}

// NewFailoverState 创建 failover 状态
func NewFailoverState(maxSwitches int, hasBoundSession bool) *FailoverState {
	return &FailoverState{
		MaxSwitches:            maxSwitches,
		FailedAccountIDs:       make(map[int64]struct{}),
		hasBoundSession:        hasBoundSession,
		profitVetoedAccountIDs: make(map[int64]struct{}),
	}
}

// RecordProfitVeto 记录一次分组利润门终检否决：把账号加入排除列表（同时登记到
// 利润否决集，使其不被 503 退避分支清掉）并递增否决计数。
//
// 返回 FailoverContinue 表示调用方可以继续重选下一个账号；返回 FailoverExhausted
// 表示本次请求的利润否决次数已达上限，调用方应按「无可用账号」终止，
// 不得继续 continue。
func (s *FailoverState) RecordProfitVeto(accountID int64) FailoverAction {
	s.FailedAccountIDs[accountID] = struct{}{}
	if s.profitVetoedAccountIDs == nil {
		s.profitVetoedAccountIDs = make(map[int64]struct{})
	}
	s.profitVetoedAccountIDs[accountID] = struct{}{}
	s.profitVetoCount++
	if s.profitVetoCount >= maxProfitVetoAttempts {
		return FailoverExhausted
	}
	return FailoverContinue
}

// ProfitVetoCount 返回本次请求累计的利润否决次数（供日志使用）。
func (s *FailoverState) ProfitVetoCount() int { return s.profitVetoCount }

// HandleFailoverError 处理 UpstreamFailoverError，返回下一步动作。
// 包含缓存计费判断、单调增加的失败排除集及换号预算。
func (s *FailoverState) HandleFailoverError(
	ctx context.Context,
	gatewayService TempUnscheduler,
	accountID int64,
	platform string,
	retryLimit int,
	failoverErr *service.UpstreamFailoverError,
) FailoverAction {
	// 客户端已断开：failover 只会用已取消的 context 重新选号并必然失败，
	// 不应再被当成账号耗尽处理（误报 502）。
	if ctx != nil && ctx.Err() != nil {
		return FailoverCanceled
	}
	s.LastFailoverErr = failoverErr
	if failoverErr == nil || !failoverErr.ShouldRetryNextAccount() {
		return FailoverExhausted
	}

	if needForceCacheBilling(s.hasBoundSession, failoverErr, false) {
		s.ForceCacheBilling = true
	}
	// 加入失败列表
	s.FailedAccountIDs[accountID] = struct{}{}

	// 检查是否耗尽
	if s.SwitchCount >= s.MaxSwitches {
		return FailoverExhausted
	}

	// 递增切换计数
	s.SwitchCount++
	logger.FromContext(ctx).Warn("gateway.failover_switch_account",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("switch_count", s.SwitchCount),
		zap.Int("max_switches", s.MaxSwitches),
	)

	return FailoverContinue
}

// HandleSelectionExhausted 在候选耗尽或取消时立即结束，始终保留本轮排除集。
func (s *FailoverState) HandleSelectionExhausted(ctx context.Context) FailoverAction {
	if ctx.Err() != nil {
		return FailoverCanceled
	}
	return FailoverExhausted
}

// needForceCacheBilling 判断 failover 时是否需要强制缓存计费。
// 粘性会话实际切换账号、或上游明确标记时，将 input_tokens 转为 cache_read 计费。
func needForceCacheBilling(hasBoundSession bool, failoverErr *service.UpstreamFailoverError, sameAccountRetry bool) bool {
	return (hasBoundSession && !sameAccountRetry) || (failoverErr != nil && failoverErr.ForceCacheBilling)
}

// failoverClientGone 判断下游客户端是否已断开（请求 context 已取消）。
// 客户端断开后 failover 必须静默终止：用已取消的 context 重新选号只会得到
// context.Canceled，并被误报成账号耗尽（通用 502）；上游 detach 的在途请求
// 照常完成计费，但不再为无人接收的响应启动新的上游尝试。
// 响应尚未提交时把状态码标记为 499（client closed request），供访问日志归类。
func failoverClientGone(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Context().Err() == nil {
		return false
	}
	// 先停 compact 心跳（接管 ResponseWriter，建立 happens-before），与
	// handleStreamingAwareError/errorResponse 等终结路径对齐，避免心跳
	// goroutine 与下面的状态标记并发触碰同一 writer。心跳已提交 200 时
	// 状态码已固化，不再标 499。
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		return true
	}
	if !c.Writer.Written() {
		c.Status(statusClientClosedRequest)
	}
	return true
}

// sleepWithContext 等待指定时长，返回 false 表示 context 已取消。
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
