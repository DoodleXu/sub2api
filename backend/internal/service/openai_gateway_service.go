package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/platform/liveattestation"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	// ChatGPT internal API for OAuth accounts
	chatgptCodexURL = "https://chatgpt.com/backend-api/codex/responses"
	// OpenAI Platform API for API Key accounts (fallback)
	openaiPlatformAPIURL            = "https://api.openai.com/v1/responses"
	openaiPlatformAPIInputTokensURL = "https://api.openai.com/v1/responses/input_tokens"
	openaiStickySessionTTL          = time.Hour // 粘性会话TTL
	// 与真实 Codex CLI 的 User-Agent 结构对齐：
	// {originator}/{version} ({OS} {OS_version}; {arch}) {terminal}
	// 缺少 OS/架构/终端后缀的形态易被上游指纹识别为非官方客户端。
	// 该后缀是 UA 形态的唯一定义处，buildCodexCLIUserAgent 按运行时版本号复用它。
	codexCLIUserAgentSuffix = " (Ubuntu 22.4.0; x86_64) xterm-256color"
	// codexCLIUserAgent 是编译期兜底 UA；运行时优先使用由后台版本号拼出的规范 UA。
	// 版本段必须来自 codexCLIVersion：UA 与 version 头是同一个版本声明的两个出口，
	// 各自硬编码会漂移成互相矛盾的身份。
	codexCLIUserAgent = openai.CodexDefaultOriginator + "/" + codexCLIVersion + codexCLIUserAgentSuffix
	// codex_cli_only 拒绝时单个请求头日志长度上限（字符）
	codexCLIOnlyHeaderValueMaxBytes = 256

	// OpenAI WS Mode 失败后的重连次数上限（不含首次尝试）。
	// 与 Codex 客户端保持一致：失败后最多重连 5 次。
	openAIWSReconnectRetryLimit = 5
	// 上游错误体只需要提取错误 JSON/日志摘要，默认 512KiB 避免错误风暴叠加大请求体。
	openAIUpstreamErrorBodyReadLimit int64 = 512 << 10
	// OpenAI WS Mode 重连退避默认值（可由配置覆盖）。
	openAIWSRetryBackoffInitialDefault = 120 * time.Millisecond
	openAIWSRetryBackoffMaxDefault     = 2 * time.Second
	openAIWSRetryJitterRatioDefault    = 0.2
	openAICompactSessionSeedKey        = "openai_compact_session_seed"
	openAIUpstreamEndpointContextKey   = "openai_actual_upstream_endpoint"
	// codexCLIVersion 是网关对上游声明的 Codex 客户端版本，同时供 codexCLIUserAgent
	// 与 version 头使用。上游 /backend-api/codex 会按客户端身份进行风控分层，
	// 陈旧版本可能被优先丢弃（HTTP 200 + 流内 server_is_overloaded）；非官方客户端配不出
	// 官方身份时整体回退到本常量，因此它必须跟随官方 CLI 的当前发布版本，
	// 落后多个版本会让这些请求稳定落在被优先丢弃的一侧。
	codexCLIVersion = "0.146.0"
	// Codex 限额快照仅用于后台展示/诊断，不需要每个成功请求都立即落库。
	openAICodexSnapshotPersistMinInterval = 30 * time.Second
	// 配额自动暂停时，超过该时长仍未刷新的 used% 快照视为陈旧，不再据此暂停账号。
	// 被暂停的账号收不到流量，其快照永远不会从上游响应头刷新；该兜底让账号在快照
	// 陈旧时放行一次请求，从而通过正常响应头自愈，而无需等待整个窗口（5h/7d）重置。
	openAICodexAutoPauseStaleAfter = 2 * time.Hour
)

// OpenAI allowed headers whitelist (for non-passthrough).
var openaiAllowedHeaders = map[string]bool{
	"accept-language":         true,
	"content-type":            true,
	"conversation_id":         true,
	"openai-beta":             true,
	"user-agent":              true,
	"originator":              true,
	"session_id":              true,
	"x-codex-beta-features":   true,
	"x-codex-installation-id": true,
	"x-codex-turn-state":      true,
	"x-codex-turn-metadata":   true,
	"x-codex-window-id":       true,
	responsesLiteHeaderKey:    true,
}

// OpenAI passthrough allowed headers whitelist.
// 透传模式下仅放行这些低风险请求头，避免将非标准/环境噪声头传给上游触发风控。
var openaiPassthroughAllowedHeaders = map[string]bool{
	"accept":                  true,
	"accept-language":         true,
	"content-type":            true,
	"conversation_id":         true,
	"openai-beta":             true,
	"user-agent":              true,
	"originator":              true,
	"session_id":              true,
	"x-codex-beta-features":   true,
	"x-codex-installation-id": true,
	"x-codex-turn-state":      true,
	"x-codex-turn-metadata":   true,
	"x-codex-window-id":       true,
	responsesLiteHeaderKey:    true,
}

// codex_cli_only 拒绝时记录的请求头白名单（仅用于诊断日志，不参与上游透传）
var codexCLIOnlyDebugHeaderWhitelist = []string{
	"User-Agent",
	"Content-Type",
	"Accept",
	"Accept-Language",
	"OpenAI-Beta",
	"Originator",
	"Session_ID",
	"Conversation_ID",
	"X-Request-ID",
	"X-Client-Request-ID",
	"X-Forwarded-For",
	"X-Real-IP",
}

// OpenAICodexUsageSnapshot represents Codex API usage limits from response headers
type OpenAICodexUsageSnapshot struct {
	PrimaryUsedPercent          *float64 `json:"primary_used_percent,omitempty"`
	PrimaryResetAfterSeconds    *int     `json:"primary_reset_after_seconds,omitempty"`
	PrimaryWindowMinutes        *int     `json:"primary_window_minutes,omitempty"`
	SecondaryUsedPercent        *float64 `json:"secondary_used_percent,omitempty"`
	SecondaryResetAfterSeconds  *int     `json:"secondary_reset_after_seconds,omitempty"`
	SecondaryWindowMinutes      *int     `json:"secondary_window_minutes,omitempty"`
	PrimaryOverSecondaryPercent *float64 `json:"primary_over_secondary_percent,omitempty"`
	UpdatedAt                   string   `json:"updated_at,omitempty"`
}

// NormalizedCodexLimits contains normalized 5h/7d rate limit data
type NormalizedCodexLimits struct {
	Used5hPercent   *float64
	Reset5hSeconds  *int
	Window5hMinutes *int
	Used7dPercent   *float64
	Reset7dSeconds  *int
	Window7dMinutes *int
}

// Normalize converts primary/secondary fields to canonical 5h/7d fields.
// Strategy: Compare window_minutes to determine which is 5h vs 7d.
// Returns nil if snapshot is nil or has no useful data.
func (s *OpenAICodexUsageSnapshot) Normalize() *NormalizedCodexLimits {
	if s == nil {
		return nil
	}

	result := &NormalizedCodexLimits{}

	primaryMins := 0
	secondaryMins := 0
	hasPrimaryWindow := false
	hasSecondaryWindow := false

	if s.PrimaryWindowMinutes != nil {
		primaryMins = *s.PrimaryWindowMinutes
		hasPrimaryWindow = true
	}
	if s.SecondaryWindowMinutes != nil {
		secondaryMins = *s.SecondaryWindowMinutes
		hasSecondaryWindow = true
	}

	// Determine mapping based on window_minutes
	use5hFromPrimary := false
	use7dFromPrimary := false

	if hasPrimaryWindow && hasSecondaryWindow {
		// Both known: smaller window is 5h, larger is 7d
		if primaryMins < secondaryMins {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasPrimaryWindow {
		// Only primary known: classify by threshold (<=360 min = 6h -> 5h window)
		if primaryMins <= 360 {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasSecondaryWindow {
		// Only secondary known: classify by threshold
		if secondaryMins <= 360 {
			// 5h from secondary, so primary (if any data) is 7d
			use7dFromPrimary = true
		} else {
			// 7d from secondary, so primary (if any data) is 5h
			use5hFromPrimary = true
		}
	} else {
		// No window_minutes: fall back to legacy assumption (primary=7d, secondary=5h)
		use7dFromPrimary = true
	}

	// Assign values
	if use5hFromPrimary {
		result.Used5hPercent = s.PrimaryUsedPercent
		result.Reset5hSeconds = s.PrimaryResetAfterSeconds
		result.Window5hMinutes = s.PrimaryWindowMinutes
		result.Used7dPercent = s.SecondaryUsedPercent
		result.Reset7dSeconds = s.SecondaryResetAfterSeconds
		result.Window7dMinutes = s.SecondaryWindowMinutes
	} else if use7dFromPrimary {
		result.Used7dPercent = s.PrimaryUsedPercent
		result.Reset7dSeconds = s.PrimaryResetAfterSeconds
		result.Window7dMinutes = s.PrimaryWindowMinutes
		result.Used5hPercent = s.SecondaryUsedPercent
		result.Reset5hSeconds = s.SecondaryResetAfterSeconds
		result.Window5hMinutes = s.SecondaryWindowMinutes
	}

	return result
}

// OpenAIUsage represents OpenAI API response usage
type OpenAIUsage struct {
	InputTokens                 int `json:"input_tokens"`
	ImageInputTokens            int `json:"image_input_tokens,omitempty"`
	ImageCacheReadTokens        int `json:"image_cache_read_tokens,omitempty"`
	OutputTokens                int `json:"output_tokens"`
	CacheCreationInputTokens    int `json:"cache_creation_input_tokens,omitempty"`
	CacheCreation5mTokens       int `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens       int `json:"cache_creation_1h_tokens,omitempty"`
	CacheCreation30mInputTokens int `json:"cache_creation_30m_input_tokens,omitempty"`
	CacheCreation12hInputTokens int `json:"cache_creation_12h_input_tokens,omitempty"`
	CacheReadInputTokens        int `json:"cache_read_input_tokens,omitempty"`
	ImageOutputTokens           int `json:"image_output_tokens,omitempty"`

	cacheCreationInputTokensPresent bool
}

// OpenAIForwardResult represents the result of forwarding
type OpenAIForwardResult struct {
	RequestID  string
	ResponseID string
	// UpstreamHeaders 是直接上游的响应头，用于按账户配置解析上游请求标识。
	UpstreamHeaders http.Header
	Usage           OpenAIUsage
	Model           string // 原始模型（用于响应和日志显示）
	// BillingModel is the model used for cost calculation.
	// When non-empty, CalculateCost uses this instead of Model.
	// This is set by the Anthropic Messages conversion path where
	// the mapped upstream model differs from the client-facing model.
	BillingModel string
	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Empty when no mapping was applied (requested model was used as-is).
	UpstreamModel string
	// UpstreamResponseModel is captured from the raw successful upstream
	// response before any client-facing rewrite or protocol conversion.
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	// UpstreamResponseServiceTier is the tier the upstream reports having used
	// (response service_tier: "priority" / "default" / "flex" / ...); "" when not declared.
	UpstreamResponseServiceTier string
	// UpstreamEndpoint is the actual upstream API path used for this request.
	// It avoids guessing when one downstream protocol can use multiple upstream endpoints.
	UpstreamEndpoint string
	// ServiceTier 优先取上游实际响应回显的 tier；缺失时回退到最终出站 body 的
	// tier。nil 表示两者都无识别 tier。
	ServiceTier *string
	// ReasoningEffort is extracted from request body (reasoning.effort) or derived from model suffix.
	// Stored for usage records display; nil means not provided / not applicable.
	ReasoningEffort *string
	// RequestedReasoningEffort is the client-requested effort before mapping.
	RequestedReasoningEffort *string
	Stream                   bool
	OpenAIWSMode             bool
	UpstreamTerminalEvent    string
	ResponseHeaders          http.Header
	DeferredResponseStatus   int
	DeferredResponseBody     []byte
	Duration                 time.Duration
	FirstTokenMs             *int
	ImageFirstOutputMs       *int
	ClientDisconnect         bool
	ImageCount               int
	ImageSize                string
	ImageInputSize           string
	ImageOutputSize          string
	ImageOutputSizes         []string
	// ImageResponseBody is populated only for successful non-streaming image
	// responses so the gateway handler can archive the original JSON without
	// changing the client-visible payload.
	ImageResponseBody []byte
	// ImageEndpoint limits per-image billing to dedicated image endpoints.
	ImageEndpoint      bool
	ImageSizeSource    string
	ImageSizeBreakdown map[string]int
	VideoCount         int
	VideoResolution    string
	// VideoDurationSeconds 是提交时请求的生成时长（xAI 按输出秒数计费），已归一化到 1-15 秒。
	VideoDurationSeconds int
	// WebSearchCalls 是 Codex alpha/search 网页搜索调用次数（每次成功请求为 1）。
	// 上游不返回 usage 字段，>0 时走按次计费（分组单价 × 次数 × 倍率）。
	WebSearchCalls int
	// SearchCount is Grok-native web_search / tool search call count (per 1k pricing).
	SearchCount int
	// AudioUsage carries Voice billing units when present.
	AudioUsage *AudioUsage

	wsReplayInput                []json.RawMessage
	wsReplayInputExists          bool
	wsAccountFailoverReplayInput []json.RawMessage
}

func (r *OpenAIForwardResult) SucceededForScheduling() bool {
	if r == nil || !r.OpenAIWSMode || r.UpstreamTerminalEvent == "" {
		return true
	}
	return r.UpstreamTerminalEvent == "response.completed" || r.UpstreamTerminalEvent == "response.done"
}

const openAIResponsesUpstreamEndpoint = "/v1/responses"

// stampOpenAIResponsesUpstreamEndpoint records that this attempt hit the
// Responses API. OpenCode Go / CN accounts cannot derive that from inbound
// path (DeriveUpstreamEndpoint falls back to the client URL).
func stampOpenAIResponsesUpstreamEndpoint(c *gin.Context, result *OpenAIForwardResult) {
	SetActualOpenAIUpstreamEndpoint(c, openAIResponsesUpstreamEndpoint)
	if result == nil {
		return
	}
	if strings.TrimSpace(result.UpstreamEndpoint) == "" {
		result.UpstreamEndpoint = openAIResponsesUpstreamEndpoint
	}
}

// SetActualOpenAIUpstreamEndpoint records the endpoint selected by the current
// forwarding attempt. It covers error paths where no OpenAIForwardResult is
// available for usage and operations logging.
func SetActualOpenAIUpstreamEndpoint(c *gin.Context, endpoint string) {
	if c == nil {
		return
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		c.Set(openAIUpstreamEndpointContextKey, endpoint)
	}
}

// ClearActualOpenAIUpstreamEndpoint clears the endpoint recorded by a prior
// account attempt. A Gin context is shared across failover attempts.
func ClearActualOpenAIUpstreamEndpoint(c *gin.Context) {
	if c != nil {
		c.Set(openAIUpstreamEndpointContextKey, "")
	}
}

// GetActualOpenAIUpstreamEndpoint returns the endpoint recorded by the latest
// forwarding attempt in this request.
func GetActualOpenAIUpstreamEndpoint(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(openAIUpstreamEndpointContextKey)
	if !exists {
		return ""
	}
	endpoint, _ := value.(string)
	return strings.TrimSpace(endpoint)
}

type OpenAIWSRetryMetricsSnapshot struct {
	RetryAttemptsTotal            int64 `json:"retry_attempts_total"`
	RetryBackoffMsTotal           int64 `json:"retry_backoff_ms_total"`
	RetryExhaustedTotal           int64 `json:"retry_exhausted_total"`
	NonRetryableFastFallbackTotal int64 `json:"non_retryable_fast_fallback_total"`
}

type OpenAICompatibilityFallbackMetricsSnapshot struct {
	SessionHashLegacyReadFallbackTotal int64   `json:"session_hash_legacy_read_fallback_total"`
	SessionHashLegacyReadFallbackHit   int64   `json:"session_hash_legacy_read_fallback_hit"`
	SessionHashLegacyDualWriteTotal    int64   `json:"session_hash_legacy_dual_write_total"`
	SessionHashLegacyReadHitRate       float64 `json:"session_hash_legacy_read_hit_rate"`

	MetadataLegacyFallbackIsMaxTokensOneHaikuTotal int64 `json:"metadata_legacy_fallback_is_max_tokens_one_haiku_total"`
	MetadataLegacyFallbackThinkingEnabledTotal     int64 `json:"metadata_legacy_fallback_thinking_enabled_total"`
	MetadataLegacyFallbackPrefetchedStickyAccount  int64 `json:"metadata_legacy_fallback_prefetched_sticky_account_total"`
	MetadataLegacyFallbackPrefetchedStickyGroup    int64 `json:"metadata_legacy_fallback_prefetched_sticky_group_total"`
	MetadataLegacyFallbackSingleAccountRetryTotal  int64 `json:"metadata_legacy_fallback_single_account_retry_total"`
	MetadataLegacyFallbackAccountSwitchCountTotal  int64 `json:"metadata_legacy_fallback_account_switch_count_total"`
	MetadataLegacyFallbackTotal                    int64 `json:"metadata_legacy_fallback_total"`
}

type openAIWSRetryMetrics struct {
	retryAttempts            atomic.Int64
	retryBackoffMs           atomic.Int64
	retryExhausted           atomic.Int64
	nonRetryableFastFallback atomic.Int64
}

type accountWriteThrottle struct {
	minInterval time.Duration
	mu          sync.Mutex
	lastByID    map[int64]time.Time
}

func newAccountWriteThrottle(minInterval time.Duration) *accountWriteThrottle {
	return &accountWriteThrottle{
		minInterval: minInterval,
		lastByID:    make(map[int64]time.Time),
	}
}

func (t *accountWriteThrottle) Allow(id int64, now time.Time) bool {
	if t == nil || id <= 0 || t.minInterval <= 0 {
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if last, ok := t.lastByID[id]; ok && now.Sub(last) < t.minInterval {
		return false
	}
	t.lastByID[id] = now

	if len(t.lastByID) > 4096 {
		cutoff := now.Add(-4 * t.minInterval)
		for accountID, writtenAt := range t.lastByID {
			if writtenAt.Before(cutoff) {
				delete(t.lastByID, accountID)
			}
		}
	}

	return true
}

var defaultOpenAICodexSnapshotPersistThrottle = newAccountWriteThrottle(openAICodexSnapshotPersistMinInterval)

// ErrNoAvailableCompactAccounts indicates a legacy /responses/compact request
// needs compact support but no compatible account is available.
var ErrNoAvailableCompactAccounts = errors.New("no available accounts support /responses/compact")

type openAITokenCountSelectionContextKey struct{}

// OpenAIGatewayService handles OpenAI API gateway operations
type OpenAIGatewayService struct {
	accountRepo           AccountRepository
	usageLogRepo          UsageLogRepository
	usageBillingRepo      UsageBillingRepository
	userRepo              UserRepository
	userSubRepo           UserSubscriptionRepository
	cache                 GatewayCache
	cfg                   *config.Config
	codexDetector         CodexClientRestrictionDetector
	schedulerSnapshot     *SchedulerSnapshotService
	concurrencyService    *ConcurrencyService
	billingService        *BillingService
	rateLimitService      *RateLimitService
	billingCacheService   *BillingCacheService
	userGroupRateResolver *userGroupRateResolver
	httpUpstream          HTTPUpstream
	pluginManager         *PluginManager
	deferredService       *DeferredService
	openAITokenProvider   *OpenAITokenProvider
	grokTokenProvider     *GrokTokenProvider
	toolCorrector         *CodexToolCorrector
	openaiWSResolver      OpenAIWSProtocolResolver
	resolver              *ModelPricingResolver
	channelService        *ChannelService
	balanceNotifyService  *BalanceNotifyService
	settingService        *SettingService
	userPlatformQuotaRepo UserPlatformQuotaRepository
	grokVideoTaskRepo     GrokVideoTaskRepository
	liveAttestation       liveattestation.Provider
	liveAttestationCipher SecretEncryptor

	openaiWSPoolOnce               sync.Once
	openaiWSStateStoreOnce         sync.Once
	openaiSchedulerOnce            sync.Once
	openaiProxyStreamCircuitOnce   sync.Once
	openaiWSPassthroughDialerOnce  sync.Once
	openaiModelTransientOnce       sync.Once
	agentIdentityTaskMu            sync.Mutex
	openaiWSPool                   *openAIWSConnPool
	openaiWSStateStore             OpenAIWSStateStore
	openaiScheduler                OpenAIAccountScheduler
	openaiWSPassthroughDialer      openAIWSClientDialer
	openaiWSSessionPreemptions     openAIWSSessionPreemptRegistry
	openaiAccountStats             *openAIAccountRuntimeStats
	openaiModelTransient           *openAIAccountModelTransientState
	openaiProxyStreamCircuit       *openAIProxyStreamCircuit
	openaiProxyStreamFailOpenLogAt atomic.Int64

	openaiWSFallbackUntil               sync.Map // key: int64(accountID), value: time.Time
	openaiAccountRuntimeBlockUntil      sync.Map // key: int64(accountID), value: time.Time
	openaiAccountRuntimeBlockReason     sync.Map // key: int64(accountID), value: string
	openaiAccountRuntimeHardBlockUntil  sync.Map // key: int64(accountID), value: time.Time
	openaiAccountRuntimeBlockLocks      sync.Map // key: int64(accountID), value: *sync.Mutex
	openaiAccountRuntimeBlockGeneration sync.Map // key: int64(accountID), value: uint64
	openaiAccountRuntimeBlockSequence   atomic.Uint64
	openaiOAuth429RetryStartedAt        sync.Map // key: int64(accountID), value: time.Time
	grokCredentialMutationLocks         sync.Map // key: int64(accountID), value: *sync.Mutex
	openaiOAuth429WindowStartUnixNano   atomic.Int64
	openaiOAuth429WindowCount           atomic.Int64
	openaiWSRetryMetrics                openAIWSRetryMetrics
	responseHeaderFilter                *responseheaders.CompiledHeaderFilter
	codexSnapshotThrottle               *accountWriteThrottle
	openAIModelsCache                   openAIModelsCache
	openaiCompatSessionResponses        sessionMemoryCache
	openaiCompatAnthropicDigestSessions sessionMemoryCache
	// openaiCodexTurnStateOrigins: 下游会话 seed → openAICodexTurnStateOrigin，
	// 记录最近一次向该会话下发 x-codex-turn-state 的铸造账号，供出站守卫
	// 剥离跨账号回带（openai_codex_turn_state.go）。
	openaiCodexTurnStateOrigins sync.Map
	openaiCodexTurnStateWrites  atomic.Uint64
}

// NewOpenAIGatewayService creates a new OpenAIGatewayService
func NewOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *OpenAIGatewayService {
	// enforceCodexIdentityHeaders 是 HTTP / 透传 / WS / 探针 等出站路径共用的纯函数收口点，
	// 拿不到配置，故在此发布进程级开关快照。配置取反义，零值即「强制统一出口开启」。
	if cfg != nil {
		SetCodexIdentityEnforcementEnabled(!cfg.Gateway.DisableCodexIdentityEnforcement)
	}
	svc := &OpenAIGatewayService{
		accountRepo:         accountRepo,
		usageLogRepo:        usageLogRepo,
		usageBillingRepo:    usageBillingRepo,
		userRepo:            userRepo,
		userSubRepo:         userSubRepo,
		cache:               cache,
		cfg:                 cfg,
		codexDetector:       NewOpenAICodexClientRestrictionDetector(cfg),
		schedulerSnapshot:   schedulerSnapshot,
		concurrencyService:  concurrencyService,
		billingService:      billingService,
		rateLimitService:    rateLimitService,
		billingCacheService: billingCacheService,
		userGroupRateResolver: newUserGroupRateResolver(
			userGroupRateRepo,
			nil,
			resolveUserGroupRateCacheTTL(cfg),
			nil,
			"service.openai_gateway",
		),
		httpUpstream:          httpUpstream,
		deferredService:       deferredService,
		openAITokenProvider:   openAITokenProvider,
		grokTokenProvider:     grokTokenProvider,
		toolCorrector:         NewCodexToolCorrector(),
		openaiWSResolver:      NewOpenAIWSProtocolResolver(cfg),
		resolver:              resolver,
		channelService:        channelService,
		balanceNotifyService:  balanceNotifyService,
		settingService:        settingService,
		userPlatformQuotaRepo: userPlatformQuotaRepo,
		liveAttestation:       liveattestation.NewProvider(),
		liveAttestationCipher: newLiveAttestationCipher(cfg),
		responseHeaderFilter:  compileResponseHeaderFilter(cfg),
		codexSnapshotThrottle: newAccountWriteThrottle(openAICodexSnapshotPersistMinInterval),
	}
	if rateLimitService != nil {
		rateLimitService.SetAccountRuntimeBlocker(svc)
	}
	if openAITokenProvider != nil {
		openAITokenProvider.SetAccountRuntimeBlocker(svc)
	}
	svc.logOpenAIWSModeBootstrap()
	return svc
}

// SetGrokVideoTaskRepository injects the durable store for Grok async-video
// tasks. It is a setter to keep existing direct constructor users source
// compatible; production wiring always supplies the repository.
func (s *OpenAIGatewayService) SetGrokVideoTaskRepository(repo GrokVideoTaskRepository) {
	if s != nil {
		s.grokVideoTaskRepo = repo
	}
}

// ResolveChannelMapping 解析渠道级模型映射（代理到 ChannelService）
func (s *OpenAIGatewayService) ResolveChannelMapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}
	}
	return s.channelService.ResolveChannelMapping(ctx, groupID, model)
}

// IsModelRestricted 检查模型是否被渠道限制（代理到 ChannelService）
func (s *OpenAIGatewayService) IsModelRestricted(ctx context.Context, groupID int64, model string) bool {
	if s.channelService == nil {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, groupID, model)
}

// ResolveChannelMappingAndRestrict 解析渠道映射。
// 模型限制检查通常在调度阶段执行；缓存不可用时 restricted 会返回 true，
// 让仍依赖该兼容返回值的入口同样 fail-closed。
func (s *OpenAIGatewayService) ResolveChannelMappingAndRestrict(ctx context.Context, groupID *int64, model string) (ChannelMappingResult, bool) {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}, false
	}
	return s.channelService.ResolveChannelMappingAndRestrict(ctx, groupID, model)
}

// isCodexImageGenerationBridgeEnabled 决定是否为 Codex 客户端自动注入 hosted
// image_generation 工具。该工具是 OpenAI Responses 专属能力，Kimi / DeepSeek /
// MiniMax 等经 OpenAI 网关转发的原生 Responses 上游不识别，注入后会被 400 拒绝，
// 因此非 OpenAI 平台账号一律不桥接，后续再按账号 → 渠道 → 全局的优先级取开关。
func (s *OpenAIGatewayService) isCodexImageGenerationBridgeEnabled(ctx context.Context, account *Account, apiKey *APIKey) bool {
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	if override := account.CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	if s != nil && s.channelService != nil && apiKey != nil && apiKey.GroupID != nil {
		ch, err := s.channelService.GetChannelForGroup(ctx, *apiKey.GroupID)
		if err != nil {
			slog.Warn("failed to resolve codex image generation bridge channel override", "group_id", *apiKey.GroupID, "error", err)
		} else if override := ch.CodexImageGenerationBridgeOverride(PlatformOpenAI); override != nil {
			return *override
		}
	}
	return s != nil && s.cfg != nil && s.cfg.Gateway.CodexImageGenerationBridgeEnabled
}

func (s *OpenAIGatewayService) checkChannelPricingRestriction(ctx context.Context, groupID *int64, requestedModel string) bool {
	if groupID == nil || s.channelService == nil || requestedModel == "" {
		return false
	}
	mapping := s.channelService.ResolveChannelMapping(ctx, *groupID, requestedModel)
	billingModel := billingModelForRestriction(mapping.BillingModelSource, requestedModel, mapping.MappedModel)
	if billingModel == "" {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, *groupID, billingModel)
}

func (s *OpenAIGatewayService) isUpstreamModelRestrictedByChannel(ctx context.Context, groupID int64, account *Account, requestedModel string, requireCompact bool) bool {
	if s.channelService == nil {
		return false
	}
	if compactForwardModel, ok := openAIForwardModelFromContext(ctx); ok {
		requestedModel = compactForwardModel.model
		requireCompact = compactForwardModel.useCompactModelMapping
	}
	upstreamModel := resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
	if upstreamModel == "" {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, groupID, upstreamModel)
}

func (s *OpenAIGatewayService) needsUpstreamChannelRestrictionCheck(ctx context.Context, groupID *int64) bool {
	if groupID == nil || s.channelService == nil {
		return false
	}
	ch, err := s.channelService.GetChannelForGroup(ctx, *groupID)
	if err != nil {
		slog.Warn("failed to check openai channel upstream restriction", "group_id", *groupID, "error", err)
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	return ch.BillingModelSource == BillingModelSourceUpstream
}

// ReplaceModelInBody 替换请求体中的 JSON model 字段（通用 gjson/sjson 实现）。
func (s *OpenAIGatewayService) ReplaceModelInBody(body []byte, newModel string) []byte {
	return ReplaceModelInBody(body, newModel)
}

func (s *OpenAIGatewayService) getCodexSnapshotThrottle() *accountWriteThrottle {
	if s != nil && s.codexSnapshotThrottle != nil {
		return s.codexSnapshotThrottle
	}
	return defaultOpenAICodexSnapshotPersistThrottle
}

func (s *OpenAIGatewayService) billingDeps() *billingDeps {
	return &billingDeps{
		accountRepo:           s.accountRepo,
		userRepo:              s.userRepo,
		userSubRepo:           s.userSubRepo,
		billingCacheService:   s.billingCacheService,
		deferredService:       s.deferredService,
		balanceNotifyService:  s.balanceNotifyService,
		userPlatformQuotaRepo: s.userPlatformQuotaRepo,
	}
}

// CloseOpenAIWSPool 关闭 OpenAI WebSocket 连接池、后台 worker 和会话缓存清理 timer。
// 应在应用优雅关闭时调用。
func (s *OpenAIGatewayService) CloseOpenAIWSPool() {
	if s == nil {
		return
	}
	s.openaiCompatSessionResponses.Close()
	s.openaiCompatAnthropicDigestSessions.Close()
	if s.openaiWSPool != nil {
		s.openaiWSPool.Close()
	}
}

func (s *OpenAIGatewayService) InvalidateAgentIdentityWSConnections(accountID int64) {
	if pool := s.getOpenAIWSConnPool(); pool != nil {
		pool.ClearAccount(accountID)
	}
}

func (s *OpenAIGatewayService) logOpenAIWSModeBootstrap() {
	if s == nil || s.cfg == nil {
		return
	}
	wsCfg := s.cfg.Gateway.OpenAIWS
	logOpenAIWSModeInfo(
		"bootstrap enabled=%v oauth_enabled=%v apikey_enabled=%v force_http=%v responses_websockets_v2=%v responses_websockets=%v payload_log_sample_rate=%.3f event_flush_batch_size=%d event_flush_interval_ms=%d prewarm_cooldown_ms=%d retry_backoff_initial_ms=%d retry_backoff_max_ms=%d retry_jitter_ratio=%.3f retry_total_budget_ms=%d ws_read_limit_bytes=%d",
		wsCfg.Enabled,
		wsCfg.OAuthEnabled,
		wsCfg.APIKeyEnabled,
		wsCfg.ForceHTTP,
		wsCfg.ResponsesWebsocketsV2,
		wsCfg.ResponsesWebsockets,
		wsCfg.PayloadLogSampleRate,
		wsCfg.EventFlushBatchSize,
		wsCfg.EventFlushIntervalMS,
		wsCfg.PrewarmCooldownMS,
		wsCfg.RetryBackoffInitialMS,
		wsCfg.RetryBackoffMaxMS,
		wsCfg.RetryJitterRatio,
		wsCfg.RetryTotalBudgetMS,
		openAIWSMessageReadLimitBytes,
	)
}

func (s *OpenAIGatewayService) getCodexClientRestrictionDetector() CodexClientRestrictionDetector {
	if s != nil && s.codexDetector != nil {
		return s.codexDetector
	}
	var cfg *config.Config
	if s != nil {
		cfg = s.cfg
	}
	return NewOpenAICodexClientRestrictionDetector(cfg)
}

func (s *OpenAIGatewayService) getOpenAIWSProtocolResolver() OpenAIWSProtocolResolver {
	if s != nil && s.openaiWSResolver != nil {
		return s.openaiWSResolver
	}
	var cfg *config.Config
	if s != nil {
		cfg = s.cfg
	}
	return NewOpenAIWSProtocolResolver(cfg)
}

func classifyOpenAIWSReconnectReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var fallbackErr *openAIWSFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return "", false
	}
	reason := strings.TrimSpace(fallbackErr.Reason)
	if reason == "" {
		return "", false
	}

	baseReason := strings.TrimPrefix(reason, "prewarm_")

	switch baseReason {
	case "policy_violation",
		"message_too_big",
		"upgrade_required",
		"ws_unsupported",
		"auth_failed",
		"invalid_encrypted_content",
		"previous_response_not_found":
		return reason, false
	}

	switch baseReason {
	case "read_event",
		"write_request",
		"write",
		"acquire_timeout",
		"acquire_conn",
		"conn_queue_full",
		"dial_failed",
		"upstream_5xx",
		"event_error",
		"error_event",
		"upstream_error_event",
		"ws_connection_limit_reached",
		"missing_final_response":
		return reason, true
	default:
		return reason, false
	}
}

func resolveOpenAIWSFallbackErrorResponse(err error) (statusCode int, errType string, clientMessage string, upstreamMessage string, ok bool) {
	if err == nil {
		return 0, "", "", "", false
	}
	var fallbackErr *openAIWSFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return 0, "", "", "", false
	}

	reason := strings.TrimSpace(fallbackErr.Reason)
	reason = strings.TrimPrefix(reason, "prewarm_")
	if reason == "" {
		return 0, "", "", "", false
	}

	var dialErr *openAIWSDialError
	if fallbackErr.Err != nil && errors.As(fallbackErr.Err, &dialErr) && dialErr != nil {
		if dialErr.StatusCode > 0 {
			statusCode = dialErr.StatusCode
		}
		if dialErr.Err != nil {
			upstreamMessage = sanitizeUpstreamErrorMessage(strings.TrimSpace(dialErr.Err.Error()))
		}
	}

	switch reason {
	case "invalid_encrypted_content":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
		errType = "invalid_request_error"
		if upstreamMessage == "" {
			upstreamMessage = "encrypted content could not be verified"
		}
	case "previous_response_not_found":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
		errType = "invalid_request_error"
		if upstreamMessage == "" {
			upstreamMessage = "previous response not found"
		}
	case "upgrade_required":
		if statusCode == 0 {
			statusCode = http.StatusUpgradeRequired
		}
	case "ws_unsupported":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
	case "auth_failed":
		if statusCode == 0 {
			statusCode = http.StatusUnauthorized
		}
	case "upstream_rate_limited":
		if statusCode == 0 {
			statusCode = http.StatusTooManyRequests
		}
	default:
		if statusCode == 0 {
			return 0, "", "", "", false
		}
	}

	if upstreamMessage == "" && fallbackErr.Err != nil {
		upstreamMessage = sanitizeUpstreamErrorMessage(strings.TrimSpace(fallbackErr.Err.Error()))
	}
	if upstreamMessage == "" {
		switch reason {
		case "upgrade_required":
			upstreamMessage = "upstream websocket upgrade required"
		case "ws_unsupported":
			upstreamMessage = "upstream websocket not supported"
		case "auth_failed":
			upstreamMessage = "upstream authentication failed"
		case "upstream_rate_limited":
			upstreamMessage = "upstream rate limit exceeded, please retry later"
		default:
			upstreamMessage = "Upstream request failed"
		}
	}

	if errType == "" {
		if statusCode == http.StatusTooManyRequests {
			errType = "rate_limit_error"
		} else {
			errType = "upstream_error"
		}
	}
	clientMessage = upstreamMessage
	return statusCode, errType, clientMessage, upstreamMessage, true
}

func (s *OpenAIGatewayService) writeOpenAIWSFallbackErrorResponse(c *gin.Context, account *Account, wsErr error) bool {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return false
	}
	statusCode, errType, clientMessage, upstreamMessage, ok := resolveOpenAIWSFallbackErrorResponse(wsErr)
	if !ok {
		return false
	}
	if strings.TrimSpace(clientMessage) == "" {
		clientMessage = "Upstream request failed"
	}
	if strings.TrimSpace(upstreamMessage) == "" {
		upstreamMessage = clientMessage
	}

	setOpsUpstreamError(c, statusCode, upstreamMessage, "")
	if account != nil {
		proxyID, proxyName := opsUpstreamWSProxyAttribution(account)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            proxyID,
			ProxyName:          proxyName,
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: statusCode,
			Kind:               "ws_error",
			Message:            upstreamMessage,
		})
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": clientMessage,
		},
	})
	return true
}

func (s *OpenAIGatewayService) openAIWSRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	initial := openAIWSRetryBackoffInitialDefault
	maxBackoff := openAIWSRetryBackoffMaxDefault
	jitterRatio := openAIWSRetryJitterRatioDefault
	if s != nil && s.cfg != nil {
		wsCfg := s.cfg.Gateway.OpenAIWS
		if wsCfg.RetryBackoffInitialMS > 0 {
			initial = time.Duration(wsCfg.RetryBackoffInitialMS) * time.Millisecond
		}
		if wsCfg.RetryBackoffMaxMS > 0 {
			maxBackoff = time.Duration(wsCfg.RetryBackoffMaxMS) * time.Millisecond
		}
		if wsCfg.RetryJitterRatio >= 0 {
			jitterRatio = wsCfg.RetryJitterRatio
		}
	}
	if initial <= 0 {
		return 0
	}
	if maxBackoff <= 0 {
		maxBackoff = initial
	}
	if maxBackoff < initial {
		maxBackoff = initial
	}
	if jitterRatio < 0 {
		jitterRatio = 0
	}
	if jitterRatio > 1 {
		jitterRatio = 1
	}

	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	backoff := initial
	if shift > 0 {
		backoff = initial * time.Duration(1<<shift)
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	if jitterRatio <= 0 {
		return backoff
	}
	jitter := time.Duration(float64(backoff) * jitterRatio)
	if jitter <= 0 {
		return backoff
	}
	delta := time.Duration(rand.Int63n(int64(jitter)*2+1)) - jitter
	withJitter := backoff + delta
	if withJitter < 0 {
		return 0
	}
	return withJitter
}

func (s *OpenAIGatewayService) openAIWSRetryTotalBudget() time.Duration {
	if s != nil && s.cfg != nil {
		ms := s.cfg.Gateway.OpenAIWS.RetryTotalBudgetMS
		if ms <= 0 {
			return 0
		}
		return time.Duration(ms) * time.Millisecond
	}
	return 0
}

func (s *OpenAIGatewayService) recordOpenAIWSRetryAttempt(backoff time.Duration) {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.retryAttempts.Add(1)
	if backoff > 0 {
		s.openaiWSRetryMetrics.retryBackoffMs.Add(backoff.Milliseconds())
	}
}

func (s *OpenAIGatewayService) recordOpenAIWSRetryExhausted() {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.retryExhausted.Add(1)
}

func (s *OpenAIGatewayService) recordOpenAIWSNonRetryableFastFallback() {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.nonRetryableFastFallback.Add(1)
}

func (s *OpenAIGatewayService) SnapshotOpenAIWSRetryMetrics() OpenAIWSRetryMetricsSnapshot {
	if s == nil {
		return OpenAIWSRetryMetricsSnapshot{}
	}
	return OpenAIWSRetryMetricsSnapshot{
		RetryAttemptsTotal:            s.openaiWSRetryMetrics.retryAttempts.Load(),
		RetryBackoffMsTotal:           s.openaiWSRetryMetrics.retryBackoffMs.Load(),
		RetryExhaustedTotal:           s.openaiWSRetryMetrics.retryExhausted.Load(),
		NonRetryableFastFallbackTotal: s.openaiWSRetryMetrics.nonRetryableFastFallback.Load(),
	}
}

func SnapshotOpenAICompatibilityFallbackMetrics() OpenAICompatibilityFallbackMetricsSnapshot {
	legacyReadFallbackTotal, legacyReadFallbackHit, legacyDualWriteTotal := openAIStickyCompatStats()
	isMaxTokensOneHaiku, thinkingEnabled, prefetchedStickyAccount, prefetchedStickyGroup, singleAccountRetry, accountSwitchCount := RequestMetadataFallbackStats()

	readHitRate := float64(0)
	if legacyReadFallbackTotal > 0 {
		readHitRate = float64(legacyReadFallbackHit) / float64(legacyReadFallbackTotal)
	}
	metadataFallbackTotal := isMaxTokensOneHaiku + thinkingEnabled + prefetchedStickyAccount + prefetchedStickyGroup + singleAccountRetry + accountSwitchCount

	return OpenAICompatibilityFallbackMetricsSnapshot{
		SessionHashLegacyReadFallbackTotal: legacyReadFallbackTotal,
		SessionHashLegacyReadFallbackHit:   legacyReadFallbackHit,
		SessionHashLegacyDualWriteTotal:    legacyDualWriteTotal,
		SessionHashLegacyReadHitRate:       readHitRate,

		MetadataLegacyFallbackIsMaxTokensOneHaikuTotal: isMaxTokensOneHaiku,
		MetadataLegacyFallbackThinkingEnabledTotal:     thinkingEnabled,
		MetadataLegacyFallbackPrefetchedStickyAccount:  prefetchedStickyAccount,
		MetadataLegacyFallbackPrefetchedStickyGroup:    prefetchedStickyGroup,
		MetadataLegacyFallbackSingleAccountRetryTotal:  singleAccountRetry,
		MetadataLegacyFallbackAccountSwitchCountTotal:  accountSwitchCount,
		MetadataLegacyFallbackTotal:                    metadataFallbackTotal,
	}
}

func (s *OpenAIGatewayService) detectCodexClientRestriction(c *gin.Context, account *Account, body []byte) CodexClientRestrictionDetectionResult {
	policy := CodexRestrictionPolicy{EngineFingerprintSignals: openai.DefaultEngineFingerprintSignals}
	if account != nil && account.IsCodexCLIOnlyEnabled() && s != nil && s.settingService != nil {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		policy = s.settingService.GetCodexRestrictionPolicy(ctx)
	}
	return s.getCodexClientRestrictionDetector().Detect(c, account, policy, body)
}

func getAPIKeyIDFromContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	v, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	apiKey, ok := v.(*APIKey)
	if !ok || apiKey == nil {
		return 0
	}
	return apiKey.ID
}

// isolateOpenAISessionID 将 apiKeyID 混入 session 标识符，
// 确保不同 API Key 的用户即使使用相同的原始 session_id/conversation_id，
// 到达上游的标识符也不同，防止跨用户会话碰撞。
func isolateOpenAISessionID(apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	h := xxhash.New()
	_, _ = fmt.Fprintf(h, "k%d:", apiKeyID)
	_, _ = h.WriteString(raw)
	return fmt.Sprintf("%016x", h.Sum64())
}

func logCodexCLIOnlyDetection(ctx context.Context, c *gin.Context, account *Account, apiKeyID int64, result CodexClientRestrictionDetectionResult, body []byte) {
	if !result.Enabled {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.Bool("codex_cli_only_enabled", result.Enabled),
		zap.Bool("codex_official_client_match", result.Matched),
		zap.String("reject_reason", result.Reason),
	}
	if apiKeyID > 0 {
		fields = append(fields, zap.Int64("api_key_id", apiKeyID))
	}
	if !result.Matched {
		fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, body)
	}
	log := logger.FromContext(ctx).With(fields...)
	if result.Matched {
		log.Info("OpenAI codex_cli_only 放行请求")
		return
	}
	log.Warn("OpenAI codex_cli_only 拒绝非官方客户端请求")
}

func appendCodexCLIOnlyRejectedRequestFields(fields []zap.Field, c *gin.Context, body []byte) []zap.Field {
	if c == nil || c.Request == nil {
		return fields
	}

	req := c.Request
	requestModel, requestStream, promptCacheKey := extractOpenAIRequestMetaFromBody(body)
	fields = append(fields,
		zap.String("request_method", strings.TrimSpace(req.Method)),
		zap.String("request_path", strings.TrimSpace(req.URL.Path)),
		zap.String("request_query", strings.TrimSpace(req.URL.RawQuery)),
		zap.String("request_host", strings.TrimSpace(req.Host)),
		zap.String("request_client_ip", strings.TrimSpace(ip.GetClientIP(c))),
		zap.String("request_remote_addr", strings.TrimSpace(req.RemoteAddr)),
		zap.String("request_user_agent", strings.TrimSpace(req.Header.Get("User-Agent"))),
		zap.String("request_content_type", strings.TrimSpace(req.Header.Get("Content-Type"))),
		zap.Int64("request_content_length", req.ContentLength),
		zap.Bool("request_stream", requestStream),
	)
	if requestModel != "" {
		fields = append(fields, zap.String("request_model", requestModel))
	}
	if promptCacheKey != "" {
		fields = append(fields, zap.String("request_prompt_cache_key_sha256", hashSensitiveValueForLog(promptCacheKey)))
	}

	if headers := snapshotCodexCLIOnlyHeaders(req.Header); len(headers) > 0 {
		fields = append(fields, zap.Any("request_headers", headers))
	}
	fields = append(fields, zap.Int("request_body_size", len(body)))
	return fields
}

func snapshotCodexCLIOnlyHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	result := make(map[string]string, len(codexCLIOnlyDebugHeaderWhitelist))
	for _, key := range codexCLIOnlyDebugHeaderWhitelist {
		value := strings.TrimSpace(header.Get(key))
		if value == "" {
			continue
		}
		result[strings.ToLower(key)] = truncateString(value, codexCLIOnlyHeaderValueMaxBytes)
	}
	return result
}

func hashSensitiveValueForLog(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

const openAIUpstreamAccessUnavailableClientMessage = "Upstream access is temporarily unavailable, please retry later"
const OpenAIUpstreamAccessStateReason = GatewayFailureReason("openai_upstream_access_state")

func OpenAIUpstreamAccessUnavailableClientMessage() string {
	return openAIUpstreamAccessUnavailableClientMessage
}

func normalizeOpenAICompatiblePlatform(platform string) string {
	return NormalizeOpenAICompatiblePlatform(platform)
}

func (s *OpenAIGatewayService) selectAccountForModelCandidates(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, preferLowUpstreamRate bool) (*Account, error) {
	platform = normalizeOpenAICompatiblePlatform(platform)
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	// 1. 尝试粘性会话命中
	// Try sticky session hit

	if account := s.tryStickySessionHit(ctx, groupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability); account != nil {
		return account, nil
	}

	// 2. 获取可调度的 OpenAI 账号
	// Get schedulable OpenAI accounts
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}

	// 3. 按优先级 + LRU 选择最佳账号
	// Select by priority + LRU
	selected, compactBlocked, filterStats := s.selectBestAccount(ctx, groupID, platform, accounts, requestedModel, excludedIDs, requireCompact, requiredCapability, preferLowUpstreamRate)

	if selected == nil {
		return nil, noAvailableOpenAISelectionError(requestedModel, compactBlocked, filterStats.summary(""))
	}

	hydrated, err := s.hydrateSelectedAccount(ctx, selected)
	if err != nil {
		return nil, err
	}

	// 利润门下推迟到 handler 终检通过后再建立新粘性绑定。
	if sessionHash != "" && !gatewayProfitControlGateActive(ctx) {
		_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, selected.ID, openaiStickySessionTTL)
	}

	return hydrated, nil
}

// GetAccessToken gets the access token for an OpenAI account
func (s *OpenAIGatewayService) GetAccessToken(ctx context.Context, account *Account) (string, string, error) {
	if account == nil {
		return "", "", errors.New("account is nil")
	}
	if account.IsShadow() {
		credAccount, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return "", "", err
		}
		account = credAccount
	}
	if account.Platform == PlatformGrok && account.Type == AccountTypeOAuth {
		if s.grokTokenProvider != nil {
			accessToken, err := s.grokTokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		accessToken := account.GetGrokAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	}

	switch account.Type {
	case AccountTypeOAuth:
		if account.IsOpenAIAgentIdentity() {
			return "", OpenAIAuthModeAgentIdentity, nil
		}
		// 使用 TokenProvider 获取缓存的 token
		if s.openAITokenProvider != nil {
			accessToken, err := s.openAITokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		// 降级：TokenProvider 未配置时直接从账号读取
		accessToken := account.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case AccountTypeSetupToken:
		if !account.IsOpenAIOAuthLike() {
			return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
		}
		// OpenAI setup tokens are inference-only bearer credentials. They use the
		// Codex OAuth forwarding protocol but have no refresh-token lifecycle.
		accessToken := account.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case AccountTypeAPIKey:
		if account.Platform == PlatformGrok {
			apiKey := strings.TrimSpace(account.GetCredential("api_key"))
			if apiKey == "" {
				return "", "", errors.New("api_key not found in credentials")
			}
			return apiKey, "apikey", nil
		}
		apiKey := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	default:
		return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

func isOpenAIImageGenerationDisabledUpstreamError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if strings.Contains(msg, "image generation is not enabled for this group") {
		return true
	}
	return strings.Contains(strings.ToLower(string(upstreamBody)), "image generation is not enabled for this group")
}

// handleErrorResponsePassthroughWithRulesBody 用于调用方已经按端点上限读取完整错误体的场景，
// 避免再次通过通用错误日志上限读取并截断客户端响应。
func (s *OpenAIGatewayService) handleErrorResponsePassthroughWithRulesBody(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	responseBody []byte,
) error {
	return s.handleErrorResponsePassthroughBodyMode(ctx, resp, c, account, requestBody, responseBody, true)
}

func (s *OpenAIGatewayService) handleErrorResponsePassthroughBodyMode(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	body []byte,
	applyRules bool,
) error {
	if failure := s.rateLimitService.BalanceFailover(ctx, c, account, resp.StatusCode, body); failure != nil {
		return failure
	}

	MarkResponseCommitted(c)

	// cyber_policy：透传账号本就把原始 body 回给客户端（下方 c.Data），此处仅打标记，
	// 供 handler 事后写风控/邮件。cyber 是上游网络安全策略拦截，不冷却账号，
	// 故下方跳过 handleOpenAIAccountUpstreamError（避免自定义 temp-unschedulable 规则误冷却）。
	cyberHit, cyberCode, cyberMsg := detectOpenAICyberPolicy(body)
	if cyberHit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           cyberCode,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)
	ruleStatus := resp.StatusCode
	ruleErrType := "upstream_error"
	ruleErrMsg := upstreamMsg
	ruleMatched := false
	if applyRules && !cyberHit {
		ruleStatus, ruleErrType, ruleErrMsg, ruleMatched = applyErrorPassthroughRule(
			c,
			PlatformOpenAI,
			resp.StatusCode,
			body,
			resp.StatusCode,
			"upstream_error",
			upstreamMsg,
		)
	}
	// 透传模式保留原始上游错误响应，但运行态账号状态仍需更新，
	// 避免粘性路由继续复用刚被限流的账号。cyber 与已命中错误规则的响应例外：
	// 前者是内容策略而非账号故障，后者与标准 OpenAI 错误路径一致，由规则接管。
	if !cyberHit && !ruleMatched {
		reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
		_ = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, reqModel)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          !ruleMatched,
		Kind:                 "http_error",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	if ruleMatched {
		c.JSON(ruleStatus, gin.H{"error": gin.H{"type": ruleErrType, "message": ruleErrMsg}})
		return fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, body)

	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

func (s *OpenAIGatewayService) recordOpenAIStreamUpstreamErrorWithStatus(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	kind string,
	payload []byte,
	message string,
	statusCode int,
) string {
	detail := ""
	if len(payload) > 0 && s != nil && s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(payload), maxBytes)
	}
	if c != nil {
		setOpsUpstreamError(c, statusCode, message, detail)
		event := OpsUpstreamErrorEvent{
			Platform:           PlatformOpenAI,
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			UpstreamStatusCode: statusCode,
			UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
			Passthrough:        passthrough,
			Kind:               kind,
			Message:            message,
			Detail:             detail,
		}
		if account != nil {
			event.Platform = account.Platform
			event.AccountID = account.ID
			event.AccountName = account.Name
		}
		appendOpsUpstreamError(c, event)
	}
	return message
}

func openAIResponseFailedSemanticStatus(payload []byte, message string) int {
	combined := strings.ToLower(strings.TrimSpace(message + " " +
		gjson.GetBytes(payload, "response.error.code").String() + " " +
		gjson.GetBytes(payload, "error.code").String() + " " +
		gjson.GetBytes(payload, "response.error.type").String() + " " +
		gjson.GetBytes(payload, "error.type").String()))
	if isOpenAIContextWindowError(message, payload) {
		return http.StatusBadRequest
	}
	if strings.Contains(combined, "rate_limit") || strings.Contains(combined, "rate limit") {
		return http.StatusTooManyRequests
	}
	for _, marker := range []string{"invalid_request", "content_policy", "policy", "safety", "not allowed", "violat"} {
		if strings.Contains(combined, marker) {
			return http.StatusBadRequest
		}
	}
	return http.StatusBadGateway
}

func (s *OpenAIGatewayService) writeOpenAIResponseFailedCompatError(
	c *gin.Context,
	account *Account,
	payload []byte,
	message string,
	writeError compatErrorWriter,
) (string, bool) {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI upstream response failed"
	}
	if c == nil || c.Writer == nil || c.Writer.Written() || writeError == nil {
		return message, false
	}
	platform := PlatformOpenAI
	if account != nil && account.Platform != "" {
		platform = account.Platform
	}
	upstreamStatus := openAIResponseFailedSemanticStatus(payload, message)
	setOpsUpstreamError(c, upstreamStatus, message, "")
	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c, platform, upstreamStatus, payload,
		http.StatusBadGateway, "upstream_error", message,
	); matched {
		if strings.TrimSpace(errMsg) == "" {
			errMsg = message
		}
		MarkResponseCommitted(c)
		writeError(c, status, errType, errMsg)
		return errMsg, true
	}
	return message, false
}

func (s *OpenAIGatewayService) getToolCorrector() *CodexToolCorrector {
	if s != nil && s.toolCorrector != nil {
		return s.toolCorrector
	}
	return NewCodexToolCorrector()
}

func normalizeOpenAIResponsesInputArgumentsInBody(body []byte) ([]byte, bool, error) {
	if !openAIResponsesInputArgumentsMayNeedNormalize(body) {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("normalize function call arguments: %w", err)
	}
	if !normalizeOpenAIResponsesInputArguments(reqBody) {
		return body, false, nil
	}
	normalized, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize function call arguments: %w", err)
	}
	return normalized, true, nil
}

func sanitizeOpenAIResponsesInputNamespaceFieldsInBody(body []byte) ([]byte, bool, error) {
	if !openAIResponsesInputNamespaceFieldsMayNeedSanitize(body) {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("sanitize input namespace fields: %w", err)
	}
	if !sanitizeOpenAIResponsesInputNamespaceFields(reqBody) {
		return body, false, nil
	}
	sanitized, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize sanitized input namespace fields: %w", err)
	}
	return sanitized, true, nil
}

func openAIResponsesInputNamespaceFieldsMayNeedSanitize(body []byte) bool {
	return bytes.Contains(body, []byte(`"input"`)) && bytes.Contains(body, []byte(`"namespace"`))
}

func sanitizeOpenAIResponsesInputNamespaceFields(reqBody map[string]any) bool {
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}

	changed := false
	for _, raw := range input {
		if sanitizeOpenAIResponsesInputNamespaceField(raw) {
			changed = true
		}
	}
	return changed
}

func sanitizeOpenAIResponsesInputNamespaceField(raw any) bool {
	item, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := item["namespace"]; !ok {
		return false
	}
	delete(item, "namespace")
	return true
}

var openAIResponsesInputArgumentNormalizeTypeTokens = [][]byte{
	[]byte(`"function_call"`),
	[]byte(`"custom_tool_call"`),
	[]byte(`"tool_call"`),
	[]byte(`"local_shell_call"`),
	[]byte(`"tool_search_call"`),
	[]byte(`"mcp_tool_call"`),
}

func openAIResponsesInputArgumentsMayNeedNormalize(body []byte) bool {
	if !bytes.Contains(body, []byte(`"arguments"`)) {
		return false
	}
	for _, token := range openAIResponsesInputArgumentNormalizeTypeTokens {
		if bytes.Contains(body, token) {
			return true
		}
	}
	return false
}

func normalizeOpenAIResponsesInputArguments(reqBody map[string]any) bool {
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}

	changed := false
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		switch itemType {
		case "function_call", "custom_tool_call":
			switch arguments := item["arguments"].(type) {
			case map[string]any, []any:
				encoded, err := json.Marshal(arguments)
				if err != nil {
					continue
				}
				item["arguments"] = string(encoded)
				changed = true
			}
		case "tool_call", "local_shell_call", "tool_search_call", "mcp_tool_call":
			if arguments, ok := item["arguments"].(string); ok {
				var decoded map[string]any
				trimmed := strings.TrimSpace(arguments)
				if trimmed == "" {
					decoded = map[string]any{}
				} else if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
					continue
				} else if decoded == nil {
					continue
				}
				item["arguments"] = decoded
				changed = true
			}
		}
	}
	return changed
}

func IsOpenAIResponsesCompactPathForTest(c *gin.Context) bool {
	return IsOpenAIResponsesCompactPath(c)
}

func normalizeOpenAICompactInputTriggerOrder(input []byte) ([]byte, bool, error) {
	if !gjson.ParseBytes(input).IsArray() {
		return input, false, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return input, false, err
	}

	nonTriggers := make([]json.RawMessage, 0, len(items))
	triggers := make([]json.RawMessage, 0, 1)
	seenTrigger := false
	needsReorder := false
	for _, item := range items {
		if gjson.GetBytes(item, "type").String() == "compaction_trigger" {
			seenTrigger = true
			triggers = append(triggers, item)
			continue
		}
		if seenTrigger {
			needsReorder = true
		}
		nonTriggers = append(nonTriggers, item)
	}
	if len(triggers) == 0 || !needsReorder {
		return input, false, nil
	}

	ordered := append(nonTriggers, triggers...)
	normalized, err := json.Marshal(ordered)
	if err != nil {
		return input, false, err
	}
	return normalized, true, nil
}

func (s *OpenAIGatewayService) recordOpenAIPricingPendingUsage(
	ctx context.Context,
	input *OpenAIRecordUsageInput,
	result *OpenAIForwardResult,
	actualInputTokens int,
	rateMultiplier float64,
) error {
	if s == nil || s.usageLogRepo == nil {
		return errors.New("usage log repository is unavailable")
	}
	if input == nil || input.APIKey == nil || input.User == nil || input.Account == nil || result == nil {
		return errors.New("pricing-pending usage metadata is incomplete")
	}

	requestID := resolveUsageBillingRequestID(ctx, result.RequestID)
	if result.OpenAIWSMode {
		if upstreamRequestID := strings.TrimSpace(result.RequestID); upstreamRequestID != "" {
			requestID = upstreamRequestID
		}
	}
	requestedModel := strings.TrimSpace(input.OriginalModel)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(result.Model)
	}
	durationMs := int(result.Duration.Milliseconds())
	billingMode := string(BillingModePricingPending)
	accountRateMultiplier := input.Account.BillingRateMultiplier()
	sentModel := upstreamSentModel(result.Model, result.UpstreamModel)
	if result.UpstreamResponseModelConflict {
		logger.L().Warn("upstream_response_model_conflict",
			zap.String("platform", input.Account.Platform),
			zap.Int64("account_id", input.Account.ID),
			zap.String("request_id", requestID),
			zap.String("sent_model", sentModel),
			zap.String("selected_response_model", strings.TrimSpace(result.UpstreamResponseModel)),
		)
	}
	usageLog := &UsageLog{
		UserID:                   input.User.ID,
		APIKeyID:                 input.APIKey.ID,
		AccountID:                input.Account.ID,
		RequestID:                requestID,
		Model:                    result.Model,
		RequestedModel:           requestedModel,
		UpstreamModel:            optionalTrimmedStringPtr(result.UpstreamModel),
		UpstreamResponseModel:    optionalTrimmedStringPtr(result.UpstreamResponseModel),
		UpstreamModelMismatch:    upstreamModelMismatch(sentModel, result.UpstreamResponseModel),
		ChannelID:                optionalInt64Ptr(input.ChannelID),
		ModelMappingChain:        optionalTrimmedStringPtr(input.ModelMappingChain),
		BillingMode:              &billingMode,
		ServiceTier:              result.ServiceTier,
		ReasoningEffort:          result.ReasoningEffort,
		RequestedReasoningEffort: coalesceRequestedReasoningEffort(result.RequestedReasoningEffort, result.ReasoningEffort),
		NativeCompactionV2:       input.NativeCompactionV2,
		InboundEndpoint:          optionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:         optionalTrimmedStringPtr(input.UpstreamEndpoint),
		InputTokens:              actualInputTokens,
		OutputTokens:             result.Usage.OutputTokens,
		CacheCreationTokens:      result.Usage.CacheCreationInputTokens,
		CacheReadTokens:          result.Usage.CacheReadInputTokens,
		ImageOutputTokens:        result.Usage.ImageOutputTokens,
		RateMultiplier:           rateMultiplier,
		AccountRateMultiplier:    &accountRateMultiplier,
		BillingType:              BillingTypeBalance,
		Stream:                   result.Stream,
		OpenAIWSMode:             result.OpenAIWSMode,
		DurationMs:               &durationMs,
		FirstTokenMs:             result.FirstTokenMs,
		ImageFirstOutputMs:       result.ImageFirstOutputMs,
		UserAgent:                optionalTrimmedStringPtr(input.UserAgent),
		IPAddress:                optionalTrimmedStringPtr(input.IPAddress),
		ImageCount:               result.ImageCount,
		ImageSize:                optionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:           optionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:          optionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:          optionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:       result.ImageSizeBreakdown,
		VideoCount:               result.VideoCount,
		VideoResolution:          optionalTrimmedStringPtr(result.VideoResolution),
		CreatedAt:                time.Now(),
	}
	if result.VideoDurationSeconds > 0 {
		usageLog.VideoDurationSeconds = &result.VideoDurationSeconds
	}
	if input.CyberBlocked {
		usageLog.RequestType = RequestTypeCyberBlocked
	}
	if input.APIKey.GroupID != nil {
		usageLog.GroupID = input.APIKey.GroupID
	}
	if input.Subscription != nil {
		usageLog.SubscriptionID = &input.Subscription.ID
		usageLog.BillingType = BillingTypeSubscription
	}
	inserted, err := s.usageLogRepo.Create(ctx, usageLog)
	if err != nil {
		return err
	}
	if !inserted {
		// request_id 幂等冲突表示同一条待对账记录已经存在，仍视为已持久化。
		return nil
	}
	return nil
}

// ValidateOpenAIUsagePricing 在请求发往上游前确认至少一个可能的计费模型可定价。
// 这是 RecordUsage 价格校验的前置防线：动态价格源或渠道价格不可用时应拒绝
// 请求，而不是在收到成功响应后生成零费用账单。
func (s *OpenAIGatewayService) ValidateOpenAIUsagePricing(
	ctx context.Context,
	apiKey *APIKey,
	account *Account,
	serviceTier string,
	compact bool,
	models ...string,
) error {
	if s == nil {
		return errors.New("OpenAI gateway service is unavailable")
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	if s.billingService == nil {
		return errors.New("OpenAI billing service is unavailable")
	}
	if apiKey == nil {
		return errors.New("API key is required for OpenAI pricing validation")
	}
	resolvedModels := make([]string, 0, len(models)*4)
	primaryEffectiveModel := ""
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		resolvedModels = append(resolvedModels, model)
		mappedModel := model
		if account != nil {
			mappedModel = strings.TrimSpace(account.GetMappedModel(model))
			resolvedModels = append(resolvedModels, mappedModel)
		}
		if compact {
			mappedModel = resolveOpenAICompactForwardModel(account, mappedModel)
			resolvedModels = append(resolvedModels, mappedModel)
		}
		effectiveModel := normalizeOpenAIModelForUpstream(account, mappedModel)
		resolvedModels = append(resolvedModels, effectiveModel)
		if primaryEffectiveModel == "" {
			primaryEffectiveModel = effectiveModel
		}
	}
	candidates := usageBillingModelCandidates("", resolvedModels...)
	if len(candidates) == 0 {
		return errors.New("OpenAI usage billing model is empty")
	}
	// 图片模型由媒体计费链路按图片规格/数量定价，不要求存在 token 价格。
	// 这里以账号映射和 compact 映射后的最终模型为准，避免把图片请求误判为缺价。
	if isOpenAIImageGenerationModel(primaryEffectiveModel) {
		return nil
	}

	var lastErr error
	longContextBillingGate := openAILongContextBillingGate(account)
	for _, candidate := range candidates {
		_, err := s.calculateOpenAIRecordUsageTokenCost(
			ctx,
			apiKey,
			candidate,
			1,
			timezone.Now(),
			UsageTokens{},
			strings.TrimSpace(serviceTier),
			longContextBillingGate,
			"",
		)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("OpenAI usage pricing unavailable for models %s: %w", strings.Join(candidates, ","), lastErr)
}
