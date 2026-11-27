package service

// 本文件由 openai_gateway_service.go 纯移动拆分而来：用量记录、计费成本计算与
// Codex 用量快照。仅做代码搬迁，无任何行为变更。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"go.uber.org/zap"
)

// OpenAIRecordUsageInput input for recording usage
type OpenAIRecordUsageInput struct {
	Result             *OpenAIForwardResult
	APIKey             *APIKey
	User               *User
	Account            *Account
	Subscription       *UserSubscription
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string // 请求的 User-Agent
	IPAddress          string // 请求的客户端 IP 地址
	SessionID          string // 客户端显式会话标识，仅用于用量行会话关联
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	QuotaPlatform      string // user×platform quota platform resolved by the handler before async billing.
	// RequireUsageLogPersistence makes RecordUsage report a final usage-log write
	// failure. It is used by async tasks whose terminal state must remain retryable
	// until their usage row is durable.
	RequireUsageLogPersistence bool
	// PricingAt 是请求级定价时刻（请求开始捕获，与利润门的定价时刻同源）。
	// 零值回退记录时刻，供未装配的图片、异步和 cyber 路径沿用。
	PricingAt time.Time
	// CyberBlocked 为 true 时把该用量行标记为 cyber（request_type=cyber），计费逻辑不变。
	CyberBlocked bool
	// NativeCompactionV2 marks native remote compaction requests for usage analytics.
	NativeCompactionV2 bool
	ChannelUsageFields
}

// CyberPolicyUsageInput 是 cyber 拒绝、未走正常 RecordUsage 的请求记录用量的入参。
// 用量按上游真实 token 计费，与 WS cyber 及正常请求口径一致（InputTokens/OutputTokens
// 取自上游 response.failed 报告的 usage，即 mark.UpstreamInTok/OutTok）。
type CyberPolicyUsageInput struct {
	APIKey       *APIKey
	Account      *Account
	Subscription *UserSubscription
	RequestID    string
	Model        string
	Stream       bool
	InputTokens  int
	OutputTokens int
	// 渠道归因与请求级 meta，使 cyber 计费行与正常 RecordUsage 行口径一致
	// （否则 cyber 行 channel_id 等为空，渠道维度统计会遗漏 cyber 命中）。
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	NativeCompactionV2 bool
	APIKeyService      APIKeyQuotaUpdater
	ChannelUsageFields
}

// RecordCyberPolicyUsageLog 为被上游 cyber_policy 拒绝、未走正常 RecordUsage 的请求
// （HTTP forward 返回错误路径）记录用量并按上游真实 token 计费，使其与 WS cyber 路径、
// 与正常请求的计费口径统一（不再是 tokens=0 免费行）。token 取自上游 response.failed
// 报告的 usage（非流式直接拒通常为 0，cost 随之为 0）。复用 RecordUsage 完成成本计算、
// 扣费与用量行写入（request_type=cyber 由 CyberBlocked 置位）。仅 forward 返回错误的
// 路径由 handler 调用，避免与成功路径的正常 RecordUsage 重复。
func (s *OpenAIGatewayService) RecordCyberPolicyUsageLog(ctx context.Context, in CyberPolicyUsageInput) {
	if s == nil || in.APIKey == nil || in.APIKey.User == nil || in.Account == nil || strings.TrimSpace(in.Model) == "" {
		return
	}
	result := &OpenAIForwardResult{
		RequestID: in.RequestID,
		Model:     in.Model,
		Stream:    in.Stream,
		Usage: OpenAIUsage{
			InputTokens:  in.InputTokens,
			OutputTokens: in.OutputTokens,
		},
	}
	if err := s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:             result,
		APIKey:             in.APIKey,
		User:               in.APIKey.User,
		Account:            in.Account,
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		SessionID:          in.SessionID,
		RequestPayloadHash: in.RequestPayloadHash,
		APIKeyService:      in.APIKeyService,
		ChannelUsageFields: in.ChannelUsageFields,
		CyberBlocked:       true,
		NativeCompactionV2: in.NativeCompactionV2,
	}); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber usage record failed: request_id=%s err=%v", in.RequestID, err)
	}
}

// RecordUsage records usage and deducts balance
// ResolveUserGroupRateMultiplier resolves the same cached multiplier used by OpenAI usage billing.
func (s *OpenAIGatewayService) ResolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if s == nil {
		return groupDefaultMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
	}
	return resolver.Resolve(ctx, userID, groupID, groupDefaultMultiplier)
}

func openAIUsagePricingAt(input *OpenAIRecordUsageInput) time.Time {
	if input != nil && !input.PricingAt.IsZero() {
		return input.PricingAt
	}
	return timezone.Now()
}

func groupBillsOpenAIFastAtStandard(apiKey *APIKey, account *Account, serviceTier string) bool {
	if apiKey == nil || apiKey.Group == nil || !apiKey.Group.FreeOpenAIFast {
		return false
	}
	if account == nil || !account.IsOpenAI() || !groupSupportsOpenAIFast(apiKey.Group.Platform) {
		return false
	}
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority", "fast":
		return true
	default:
		return false
	}
}

func (usage OpenAIUsage) billingUsageTokens() UsageTokens {
	creationTokens := usage.CacheCreationInputTokens
	if creationTokens <= 0 {
		creationTokens = max(usage.CacheCreation5mTokens, 0) + max(usage.CacheCreation1hTokens, 0)
	}
	tokens := UsageTokens{
		InputTokens:            max(usage.InputTokens-usage.CacheReadInputTokens-creationTokens, 0),
		ImageInputTokens:       max(usage.ImageInputTokens-usage.ImageCacheReadTokens, 0),
		ImageCacheReadTokens:   usage.ImageCacheReadTokens,
		OutputTokens:           usage.OutputTokens,
		CacheCreationTokens:    creationTokens,
		CacheCreation5mTokens:  usage.CacheCreation5mTokens,
		CacheCreation1hTokens:  usage.CacheCreation1hTokens,
		CacheCreation30mTokens: usage.CacheCreation30mInputTokens,
		CacheCreation12hTokens: usage.CacheCreation12hInputTokens,
		CacheReadTokens:        usage.CacheReadInputTokens,
		ImageOutputTokens:      usage.ImageOutputTokens,
	}
	return tokens
}

func (s *OpenAIGatewayService) RecordUsage(ctx context.Context, input *OpenAIRecordUsageInput) error {
	if input == nil {
		return errors.New("openai usage input is nil")
	}
	result := input.Result
	if result == nil {
		return errors.New("openai usage result is nil")
	}
	if s.rateLimitService != nil && input.Account != nil && input.Account.Platform == PlatformOpenAI {
		s.rateLimitService.ResetOpenAI403Counter(ctx, input.Account.ID)
	}

	apiKey := input.APIKey
	user := input.User
	account := input.Account
	subscription := input.Subscription
	ApplyOpenAIImageBillingResolution(result)

	// OpenAI input_tokens 是总输入，包含缓存读取和缓存写入明细。
	// 将三类 token 拆成互斥桶，避免缓存写入同时按普通输入和 cache_write 重复计费。
	tokens := result.Usage.billingUsageTokens()
	actualInputTokens := tokens.InputTokens

	// Get rate multiplier
	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if apiKey.GroupID != nil && apiKey.Group != nil {
		resolver := s.userGroupRateResolver
		if resolver == nil {
			resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
		}
		multiplier = resolver.Resolve(ctx, user.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	// token 倍率叠加高峰因子（token 计费含图片 token，图片按次倍率不受影响）。高峰因子按请求时刻现算，
	// 不并入上面的 Resolve，以免污染 user:group 倍率缓存。
	baseMultiplier := multiplier
	pricingAt := openAIUsagePricingAt(input)
	multiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, baseMultiplier, pricingAt)
	videoMultiplier := resolveVideoRateMultiplier(apiKey, baseMultiplier)

	var cost *CostBreakdown
	var err error
	billingModel := forwardResultBillingModel(result.Model, result.UpstreamModel)
	if result.BillingModel != "" {
		billingModel = strings.TrimSpace(result.BillingModel)
	}
	if input.BillingModelSource == BillingModelSourceChannelMapped && input.ChannelMappedModel != "" && input.ChannelMappedModel != input.OriginalModel {
		billingModel = input.ChannelMappedModel
	}
	if input.BillingModelSource == BillingModelSourceRequested && input.OriginalModel != "" {
		billingModel = input.OriginalModel
	}
	billingModels := usageBillingModelCandidates(
		billingModel,
		result.BillingModel,
		input.ChannelMappedModel,
		input.OriginalModel,
		result.UpstreamModel,
		result.Model,
	)
	billingModels = s.filterCNProviderBillingModelCandidates(ctx, account, apiKey, billingModels)
	billingAccount := account
	if account.IsShadow() {
		billingAccount, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return err
		}
	}
	longContextBillingGate := openAILongContextBillingGate(billingAccount)
	modelLongContextExempt := openAISubscriptionLongContextExempt(billingAccount, result)
	logServiceTierBillingDowngrade("service.openai_gateway", account, result.RequestID, ApplyOpenAIServiceTierBillingResolution(billingAccount, result))
	serviceTier := ""
	if result.ServiceTier != nil {
		serviceTier = strings.TrimSpace(*result.ServiceTier)
	}
	cost, err = s.calculateOpenAIRecordUsageCost(ctx, result, apiKey, billingModels, multiplier, imageMultiplier, videoMultiplier, baseMultiplier, tokens, serviceTier, longContextBillingGate, modelLongContextExempt, pricingAt)
	if err != nil {
		// 上游已经完成时不能只返回异步错误，否则既没有扣费，也没有可追溯记录。
		// 先持久化 pricing_pending 行，保留原始用量和完整路由信息供后续对账；
		// 它不会进入扣费事务，也不会伪装成零费用已结算账单。
		if pendingErr := s.recordOpenAIPricingPendingUsage(ctx, input, result, actualInputTokens, multiplier); pendingErr != nil {
			return fmt.Errorf("%w; persist pricing-pending usage: %v", err, pendingErr)
		}
		return err
	}
	if responseModel := responseModelBillingDeclaration(
		input.BillingModelSource,
		result.UpstreamResponseModel,
		result.UpstreamResponseModelConflict,
		result.ImageCount > 0 || result.AudioUsage != nil || result.SearchCount > 0,
	); responseModel != "" && !strings.EqualFold(responseModel, strings.TrimSpace(billingModel)) {
		responseChannelPriced := s.resolveOpenAIChannelPricing(ctx, responseModel, apiKey) != nil
		identified := responseChannelPriced || (s.billingService != nil && s.billingService.HasIdentifiedTokenPricing(responseModel))
		if identified {
			responseModels := s.filterCNProviderBillingModelCandidates(ctx, account, apiKey, usageBillingModelCandidates(responseModel))
			responseCost, responseErr := s.calculateOpenAIRecordUsageCost(ctx, result, apiKey, responseModels, multiplier, imageMultiplier, videoMultiplier, baseMultiplier, tokens, serviceTier, longContextBillingGate, modelLongContextExempt, pricingAt)
			baselineChannelPriced := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey) != nil
			if responseErr == nil && responseModelBillingAdoptable(cost, responseCost, baselineChannelPriced, responseChannelPriced) {
				cost = responseCost
				billingModels = responseModels
			}
		}
	}

	if groupBillsOpenAIFastAtStandard(apiKey, billingAccount, serviceTier) {
		standardCost, standardErr := s.calculateOpenAIRecordUsageCost(ctx, result, apiKey, billingModels, multiplier, imageMultiplier, videoMultiplier, baseMultiplier, tokens, "", longContextBillingGate, modelLongContextExempt, pricingAt)
		if standardErr != nil {
			return standardErr
		}
		if cost != nil && standardCost != nil {
			cost.ActualCost = standardCost.ActualCost
		}
	}

	// Determine billing type
	isSubscriptionBilling := subscription != nil && apiKey.Group != nil && apiKey.Group.IsSubscriptionType()
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	// Create usage log
	durationMs := int(result.Duration.Milliseconds())
	accountRateMultiplier := account.BillingRateMultiplier()
	requestID := resolveUsageBillingRequestID(ctx, result.RequestID)
	if result.OpenAIWSMode {
		if upstreamRequestID := strings.TrimSpace(result.RequestID); upstreamRequestID != "" {
			requestID = upstreamRequestID
		}
	}

	// 确定 RequestedModel（渠道映射前的原始模型）
	requestedModel := result.Model
	if input.OriginalModel != "" {
		requestedModel = input.OriginalModel
	}
	sentModel := upstreamSentModel(result.Model, result.UpstreamModel)
	if result.UpstreamResponseModelConflict {
		logger.L().Warn("upstream_response_model_conflict",
			zap.String("platform", account.Platform),
			zap.Int64("account_id", account.ID),
			zap.String("request_id", requestID),
			zap.String("sent_model", sentModel),
			zap.String("selected_response_model", strings.TrimSpace(result.UpstreamResponseModel)),
		)
	}

	usageLog := &UsageLog{
		UserID:                   user.ID,
		APIKeyID:                 apiKey.ID,
		AccountID:                account.ID,
		RequestID:                requestID,
		Model:                    result.Model,
		RequestedModel:           requestedModel,
		UpstreamModel:            optionalTrimmedStringPtr(result.UpstreamModel),
		UpstreamResponseModel:    optionalTrimmedStringPtr(result.UpstreamResponseModel),
		UpstreamModelMismatch:    upstreamModelMismatch(sentModel, result.UpstreamResponseModel),
		ServiceTier:              result.ServiceTier,
		ReasoningEffort:          result.ReasoningEffort,
		RequestedReasoningEffort: coalesceRequestedReasoningEffort(result.RequestedReasoningEffort, result.ReasoningEffort),
		NativeCompactionV2:       input.NativeCompactionV2,
		InboundEndpoint:          optionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:         optionalTrimmedStringPtr(input.UpstreamEndpoint),
		InputTokens:              actualInputTokens,
		OutputTokens:             result.Usage.OutputTokens,
		CacheCreationTokens:      result.Usage.CacheCreationInputTokens,
		CacheCreation5mTokens:    result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens:    result.Usage.CacheCreation1hTokens,
		CacheCreation30mTokens:   result.Usage.CacheCreation30mInputTokens,
		CacheCreation12hTokens:   result.Usage.CacheCreation12hInputTokens,
		CacheReadTokens:          result.Usage.CacheReadInputTokens,
		ImageOutputTokens:        result.Usage.ImageOutputTokens,
		ImageCount:               result.ImageCount,
		ImageSize:                optionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:           optionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:          optionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:          optionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:       result.ImageSizeBreakdown,
	}
	isVideoUsage := isGrokVideoUsageResult(result, billingModels)
	if isVideoUsage {
		usageLog.ImageSize = nil
		usageLog.ImageSizeSource = nil
		usageLog.VideoCount = result.VideoCount
		usageLog.VideoResolution = optionalTrimmedStringPtr(NormalizeVideoBillingResolutionOrDefault(result.VideoResolution))
		videoDurationSeconds := NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
		usageLog.VideoDurationSeconds = &videoDurationSeconds
	}
	if cost != nil {
		usageLog.InputCost = cost.InputCost
		usageLog.OutputCost = cost.OutputCost
		usageLog.ImageOutputCost = cost.ImageOutputCost
		usageLog.CacheCreationCost = cost.CacheCreationCost
		usageLog.CacheReadCost = cost.CacheReadCost
		usageLog.TotalCost = cost.TotalCost
		usageLog.ActualCost = cost.ActualCost
		usageLog.LongContextBillingApplied = cost.LongContextBillingApplied
	}
	if isVideoUsage && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = videoMultiplier
	} else if result.ImageCount > 0 && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = imageMultiplier
	} else {
		usageLog.RateMultiplier = multiplier
	}
	usageLog.AccountRateMultiplier = &accountRateMultiplier
	usageLog.BillingType = billingType
	usageLog.Stream = result.Stream
	if input.CyberBlocked {
		usageLog.RequestType = RequestTypeCyberBlocked
	}
	usageLog.OpenAIWSMode = result.OpenAIWSMode
	usageLog.DurationMs = &durationMs
	usageLog.FirstTokenMs = result.FirstTokenMs
	usageLog.ImageFirstOutputMs = result.ImageFirstOutputMs
	usageLog.CreatedAt = time.Now()
	// 设置渠道信息
	usageLog.ChannelID = optionalInt64Ptr(input.ChannelID)
	usageLog.ModelMappingChain = optionalTrimmedStringPtr(input.ModelMappingChain)
	// 设置计费模式
	if cost != nil && cost.BillingMode != "" {
		billingMode := cost.BillingMode
		usageLog.BillingMode = &billingMode
	} else if isVideoUsage {
		billingMode := string(BillingModeVideo)
		usageLog.BillingMode = &billingMode
	} else if result.ImageCount > 0 {
		billingMode := string(BillingModeImage)
		usageLog.BillingMode = &billingMode
	} else {
		billingMode := string(BillingModeToken)
		usageLog.BillingMode = &billingMode
	}
	// 添加 UserAgent / IPAddress。取副本地址：指向 input 字段会让整个 input（含 Result
	// 及其引用的请求体）随异步写入的 usageLog 一直存活。
	if userAgent := input.UserAgent; userAgent != "" {
		usageLog.UserAgent = &userAgent
	}
	if ipAddress := input.IPAddress; ipAddress != "" {
		usageLog.IPAddress = &ipAddress
	}
	usageLog.SessionID = optionalTrimmedStringPtr(input.SessionID)

	if apiKey.GroupID != nil {
		usageLog.GroupID = apiKey.GroupID
	}
	if subscription != nil {
		usageLog.SubscriptionID = &subscription.ID
	}

	// 计算账号统计定价费用（使用最终上游模型匹配自定义规则）
	if apiKey.GroupID != nil {
		applyAccountStatsCost(ctx, usageLog, s.channelService, s.billingService,
			account.ID, *apiKey.GroupID, result.UpstreamModel, result.Model,
			tokens, cost.TotalCost, pricingAt,
			accountStatsLongContextPricingEnabled(longContextBillingGate) && !modelLongContextExempt, 1,
		)
	}

	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		if err := writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway"); err != nil && input.RequireUsageLogPersistence {
			return fmt.Errorf("persist simple-mode usage log: %w", err)
		}
		if s.cfg.SimpleModeKeyRateLimitEnabled {
			_, _ = applyUsageBilling(ctx, requestID, usageLog, &postUsageBillingParams{
				Cost: cost, User: user, APIKey: apiKey, Account: account,
				AccountRateMultiplier: accountRateMultiplier, SimpleModeKeyRateLimitOnly: true,
			}, s.billingDeps(), s.usageBillingRepo)
		}
		logger.LegacyPrintf("service.openai_gateway", "[SIMPLE MODE] Usage recorded (not billed): user=%d, tokens=%d", usageLog.UserID, usageLog.TotalTokens())
		s.deferredService.ScheduleLastUsedUpdate(account.ID)
		return nil
	}

	// Async usage billing runs outside the original request context, so it
	// cannot recover ForcePlatform there. Fall back for internal/test callers.
	quotaPlatform := input.QuotaPlatform
	if quotaPlatform == "" {
		quotaPlatform = PlatformFromAPIKey(apiKey)
	}

	billingErr := func() error {
		_, err := applyUsageBilling(ctx, requestID, usageLog, &postUsageBillingParams{
			Cost:                  cost,
			User:                  user,
			APIKey:                apiKey,
			Account:               account,
			Subscription:          subscription,
			RequestPayloadHash:    resolveUsageBillingPayloadFingerprint(ctx, input.RequestPayloadHash),
			IsSubscriptionBill:    isSubscriptionBilling,
			AccountRateMultiplier: accountRateMultiplier,
			APIKeyService:         input.APIKeyService,
			Platform:              quotaPlatform,
		}, s.billingDeps(), s.usageBillingRepo)
		return err
	}()

	if billingErr != nil {
		usageLog.ActualCost = 0
		usageLog.Succeeded = boolPtr(false)
		_ = writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway")
		return billingErr
	}
	_ = writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway")

	return nil
}

func openAILongContextBillingGate(account *Account) *bool {
	if account == nil || !account.IsOpenAI() {
		return nil
	}
	enabled := account.IsOpenAILongContextBillingEnabled()
	return &enabled
}

// openAISubscriptionLongContextExempt 按实际凭据和最终发送模型判断订阅豁免。
// GPT-6.1 Sol / Terra 的订阅请求不再收取模型长上下文附加费；API Key 仍使用 API 价卡。
// 使用发送模型，避免客户别名、渠道计费模型或响应模型计费切换改变上游订阅规则。
func openAISubscriptionLongContextExempt(account *Account, result *OpenAIForwardResult) bool {
	if !account.IsOpenAIOAuthLike() || result == nil {
		return false
	}
	model := strings.TrimSpace(result.UpstreamModel)
	if model == "" {
		model = result.Model
	}
	return openai.GPT62ModelID(model) != "" || openai.IsGPT61SolModelSpelling(model) || openai.IsGPT61TerraModelSpelling(model)
}

func (s *OpenAIGatewayService) calculateOpenAIRecordUsageCost(
	ctx context.Context,
	result *OpenAIForwardResult,
	apiKey *APIKey,
	billingModels []string,
	multiplier float64,
	imageMultiplier float64,
	videoMultiplier float64,
	webSearchMultiplier float64,
	tokens UsageTokens,
	serviceTier string,
	longContextBillingGate *bool,
	modelLongContextExempt bool,
	pricingAt time.Time,
) (*CostBreakdown, error) {
	billingModel := firstUsageBillingModel(billingModels)
	if result != nil && result.WebSearchCalls > 0 {
		return s.billingService.CalculateWebSearchCost(result.WebSearchCalls, webSearchPricePerCallFromAPIKey(apiKey), webSearchMultiplier), nil
	}
	if isGrokVideoUsageResult(result, billingModels) {
		// Grok video results may also contain image_count for compatibility metadata;
		// video pricing must win so duration/resolution costs are not billed as images.
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved == nil || resolved.Mode != BillingModeToken {
			return s.calculateOpenAIVideoCost(ctx, billingModel, apiKey, result, videoMultiplier), nil
		}
	}
	if result != nil && result.VideoCount > 0 {
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil {
			gid := apiKey.Group.ID
			cost, err := s.billingService.CalculateCostUnified(CostInput{
				Ctx: ctx, Model: billingModel, GroupID: &gid, Group: apiKey.Group,
				UsageUnits: float64(result.VideoCount * result.VideoDurationSeconds), RateMultiplier: videoMultiplier,
				ReasoningEffort: optionalStringValue(result.ReasoningEffort), Resolver: s.resolver, Resolved: resolved,
			})
			if err == nil {
				return cost, nil
			}
		}
	}
	if result != nil && result.AudioUsage != nil {
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil {
			gid := apiKey.Group.ID
			cost, err := s.billingService.CalculateCostUnified(CostInput{
				Ctx: ctx, Model: billingModel, GroupID: &gid, Group: apiKey.Group,
				UsageUnits: result.AudioUsage.DurationOrUnits, RateMultiplier: multiplier,
				ReasoningEffort: optionalStringValue(result.ReasoningEffort), Resolver: s.resolver, Resolved: resolved,
			})
			if err == nil {
				return cost, nil
			}
		}
	}
	if result != nil && result.ImageCount > 0 {
		// 分组显式开启按张计费且来自图片端点时强制走图片计费；否则渠道 token 定价优先。
		// 混合的 Responses/Chat/WS 请求即使产出了图片，也不能因开关丢失文本 token 费用。
		if imageCountBillingApplies(apiKey, result.ImageEndpoint) {
			return s.calculateOpenAIImageCost(ctx, billingModel, apiKey, result, imageMultiplier), nil
		}
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved == nil || resolved.Mode != BillingModeToken {
			return s.calculateOpenAIImageCost(ctx, billingModel, apiKey, result, imageMultiplier), nil
		}
	}

	var tokenCost *CostBreakdown
	var lastErr error
	if len(billingModels) > 0 && billingModel != "" {
		for _, candidate := range billingModels {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			cost, err := s.calculateOpenAIRecordUsageTokenCost(ctx, apiKey, candidate, multiplier, pricingAt, tokens, serviceTier, longContextBillingGate, optionalStringValue(result.ReasoningEffort), modelLongContextExempt)
			if err == nil {
				tokenCost = cost
				break
			}
			lastErr = err
		}
	}

	searchCost := (*CostBreakdown)(nil)
	if result != nil && result.SearchCount > 0 {
		searchCost = s.billingService.CalculateSearchCost(result.SearchCount, groupSearchPricePer1kFromAPIKey(apiKey), webSearchMultiplier)
	}

	tokenBillingAttempted := len(billingModels) > 0 && billingModel != ""
	if tokenCost == nil {
		if tokenBillingAttempted {
			if lastErr == nil {
				lastErr = fmt.Errorf("%w: no non-empty billing model candidates", ErrModelPricingUnavailable)
			}
			return nil, fmt.Errorf("calculate OpenAI usage cost failed for billing models %s: %w", strings.Join(billingModels, ","), lastErr)
		}
		if searchCost != nil {
			return searchCost, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("%w: openai usage billing model is empty", ErrModelPricingUnavailable)
		}
		return nil, fmt.Errorf("calculate OpenAI usage cost failed for billing models %s: %w", strings.Join(billingModels, ","), lastErr)
	}
	if searchCost == nil || (searchCost.TotalCost == 0 && searchCost.ActualCost == 0) {
		return tokenCost, nil
	}
	tokenCost.TotalCost += searchCost.TotalCost
	tokenCost.ActualCost += searchCost.ActualCost
	return tokenCost, nil
}

func isGrokVideoBillingModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video")
}

func isGrokVideoUsageResult(result *OpenAIForwardResult, billingModels []string) bool {
	if result == nil || result.VideoCount <= 0 {
		return false
	}
	candidates := append([]string{}, billingModels...)
	candidates = append(candidates, result.BillingModel, result.Model, result.UpstreamModel)
	for _, candidate := range candidates {
		if isGrokVideoBillingModel(candidate) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) calculateOpenAIRecordUsageTokenCost(
	ctx context.Context,
	apiKey *APIKey,
	billingModel string,
	multiplier float64,
	pricingAt time.Time,
	tokens UsageTokens,
	serviceTier string,
	longContextBillingGate *bool,
	reasoningEffort string,
	modelLongContextExemptArgs ...bool,
) (*CostBreakdown, error) {
	modelLongContextExempt := len(modelLongContextExemptArgs) > 0 && modelLongContextExemptArgs[0]
	input := CostInput{
		Ctx: ctx, Model: billingModel, Tokens: tokens, RequestCount: 1, RateMultiplier: multiplier,
		PricingAt: pricingAt, ServiceTier: serviceTier, LongContextBillingEnabled: longContextBillingGate,
		DisableModelLongContextPricing: modelLongContextExempt,
	}
	if s.resolver != nil && apiKey.Group != nil {
		input.GroupID = &apiKey.Group.ID
		input.Group = apiKey.Group
		input.ReasoningEffort = reasoningEffort
		input.Resolver = s.resolver
	}
	return s.billingService.CalculateCostUnified(input)
}

func (s *OpenAIGatewayService) calculateOpenAIImageCost(
	ctx context.Context,
	billingModel string,
	apiKey *APIKey,
	result *OpenAIForwardResult,
	multiplier float64,
) *CostBreakdown {
	sizeTier := NormalizeImageBillingTierOrDefault(result.ImageSize)
	groupConfig := imagePriceConfigFromAPIKey(apiKey)
	if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
		return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
	}
	if refreshed := s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey); refreshed != apiKey {
		apiKey = refreshed
		groupConfig = imagePriceConfigFromAPIKey(apiKey)
		if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
			return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
		}
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil &&
		(resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage) {
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:             ctx,
			Model:           billingModel,
			GroupID:         &gid,
			RequestCount:    result.ImageCount,
			SizeTier:        sizeTier,
			RateMultiplier:  multiplier,
			ReasoningEffort: optionalStringValue(result.ReasoningEffort),
			Resolver:        s.resolver,
			Resolved:        resolved,
		})
		if err == nil {
			return cost
		}
		logger.LegacyPrintf("service.openai_gateway", "Calculate image channel cost failed: %v", err)
	}

	return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
}

func (s *OpenAIGatewayService) calculateOpenAIVideoCost(
	ctx context.Context,
	billingModel string,
	apiKey *APIKey,
	result *OpenAIForwardResult,
	multiplier float64,
) *CostBreakdown {
	videoCount := result.VideoCount
	if videoCount <= 0 {
		videoCount = 1
	}
	resolution := NormalizeVideoBillingResolutionOrDefault(result.VideoResolution)
	durationSeconds := NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
	groupConfig := videoPriceConfigFromAPIKey(apiKey)
	if apiKeyHasConfiguredVideoPrice(apiKey, billingModel, resolution) {
		return s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, groupConfig, multiplier)
	}
	if refreshed := s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey); refreshed != apiKey {
		apiKey = refreshed
		groupConfig = videoPriceConfigFromAPIKey(apiKey)
		if apiKeyHasConfiguredVideoPrice(apiKey, billingModel, resolution) {
			return s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, groupConfig, multiplier)
		}
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil &&
		(resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage) {
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          billingModel,
			GroupID:        &gid,
			RequestCount:   videoCount,
			SizeTier:       resolution,
			RateMultiplier: multiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err == nil {
			cost.BillingMode = string(BillingModeVideo)
			return cost
		}
		logger.LegacyPrintf("service.openai_gateway", "Calculate video channel cost failed: %v", err)
	}

	return s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, groupConfig, multiplier)
}

func (s *OpenAIGatewayService) apiKeyWithFreshGroupMediaPricing(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID <= 0 {
		return apiKey
	}
	if !groupMediaPricingLooksIncomplete(apiKey.Group) {
		return apiKey
	}
	if s == nil || s.channelService == nil || s.channelService.groupRepo == nil {
		return apiKey
	}
	group, err := s.channelService.groupRepo.GetByIDLite(ctx, *apiKey.GroupID)
	if err != nil || group == nil {
		return apiKey
	}
	clone := *apiKey
	clone.Group = group
	return &clone
}

func groupMediaPricingLooksIncomplete(group *Group) bool {
	if group == nil {
		return true
	}
	if group.ImageRateIndependent || group.VideoRateIndependent {
		return false
	}
	if group.ImageRateMultiplier != 0 || group.VideoRateMultiplier != 0 {
		return false
	}
	if len(group.VideoModelPrices) > 0 {
		return false
	}
	if group.SearchPricePer1k != nil ||
		group.AudioRealtimePricePerMin != nil ||
		group.AudioTTSPricePerMillionChars != nil ||
		group.AudioSTTPricePerHour != nil ||
		group.WebSearchPricePerCall != nil {
		return false
	}
	return group.ImagePrice1K == nil && group.ImagePrice2K == nil && group.ImagePrice4K == nil &&
		group.VideoPrice480P == nil && group.VideoPrice720P == nil && group.VideoPrice1080P == nil
}

// filterCNProviderBillingModelCandidates prevents CN-compatible endpoints from
// falling back to unrelated Claude catalog pricing unless the operator has
// explicitly configured group or channel pricing for that model name.
func (s *OpenAIGatewayService) filterCNProviderBillingModelCandidates(ctx context.Context, account *Account, apiKey *APIKey, candidates []string) []string {
	if account == nil || (!account.IsCNProvider() && !account.IsOpenCodeGo()) {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "claude") &&
			s.resolveOpenAIChannelPricing(ctx, trimmed, apiKey) == nil {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func (s *OpenAIGatewayService) resolveOpenAIChannelPricing(ctx context.Context, billingModel string, apiKey *APIKey) *ResolvedPricing {
	if s.resolver == nil || apiKey == nil || apiKey.Group == nil {
		return nil
	}
	gid := apiKey.Group.ID
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &gid, Group: apiKey.Group})
	if resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel {
		return resolved
	}
	return nil
}

// ParseCodexRateLimitHeaders extracts Codex usage limits from response headers.
// Exported for use in ratelimit_service when handling OpenAI 429 responses.
func ParseCodexRateLimitHeaders(headers http.Header) *OpenAICodexUsageSnapshot {
	snapshot := &OpenAICodexUsageSnapshot{}
	hasData := false

	// Helper to parse float64 from header
	parseFloat := func(key string) *float64 {
		if v := headers.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
		return nil
	}

	// Helper to parse int from header
	parseInt := func(key string) *int {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return &i
			}
		}
		return nil
	}

	// Primary (weekly) limits
	if v := parseFloat("x-codex-primary-used-percent"); v != nil {
		snapshot.PrimaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-reset-after-seconds"); v != nil {
		snapshot.PrimaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-window-minutes"); v != nil {
		snapshot.PrimaryWindowMinutes = v
		hasData = true
	}

	// Secondary (5h) limits
	if v := parseFloat("x-codex-secondary-used-percent"); v != nil {
		snapshot.SecondaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-reset-after-seconds"); v != nil {
		snapshot.SecondaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-window-minutes"); v != nil {
		snapshot.SecondaryWindowMinutes = v
		hasData = true
	}

	// Overflow ratio
	if v := parseFloat("x-codex-primary-over-secondary-limit-percent"); v != nil {
		snapshot.PrimaryOverSecondaryPercent = v
		hasData = true
	}

	if !hasData {
		return nil
	}

	snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
	return snapshot
}

func codexSnapshotBaseTime(snapshot *OpenAICodexUsageSnapshot, fallback time.Time) time.Time {
	if snapshot == nil {
		return fallback
	}
	if snapshot.UpdatedAt == "" {
		return fallback
	}
	base, err := time.Parse(time.RFC3339, snapshot.UpdatedAt)
	if err != nil {
		return fallback
	}
	return base
}

func codexResetAtRFC3339(base time.Time, resetAfterSeconds *int) *string {
	if resetAfterSeconds == nil {
		return nil
	}
	sec := *resetAfterSeconds
	if sec < 0 {
		sec = 0
	}
	resetAt := base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
	return &resetAt
}

func buildCodexUsageExtraUpdates(snapshot *OpenAICodexUsageSnapshot, fallbackNow time.Time) map[string]any {
	if snapshot == nil {
		return nil
	}

	baseTime := codexSnapshotBaseTime(snapshot, fallbackNow)
	updates := make(map[string]any)

	// 保存原始 primary/secondary 字段，便于排查问题
	if snapshot.PrimaryUsedPercent != nil {
		updates["codex_primary_used_percent"] = *snapshot.PrimaryUsedPercent
	}
	if snapshot.PrimaryResetAfterSeconds != nil {
		updates["codex_primary_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
	}
	if snapshot.PrimaryWindowMinutes != nil {
		updates["codex_primary_window_minutes"] = *snapshot.PrimaryWindowMinutes
	}
	if snapshot.SecondaryUsedPercent != nil {
		updates["codex_secondary_used_percent"] = *snapshot.SecondaryUsedPercent
	}
	if snapshot.SecondaryResetAfterSeconds != nil {
		updates["codex_secondary_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
	}
	if snapshot.SecondaryWindowMinutes != nil {
		updates["codex_secondary_window_minutes"] = *snapshot.SecondaryWindowMinutes
	}
	if snapshot.PrimaryOverSecondaryPercent != nil {
		updates["codex_primary_over_secondary_percent"] = *snapshot.PrimaryOverSecondaryPercent
	}
	updates["codex_usage_updated_at"] = baseTime.Format(time.RFC3339)

	// 归一化到 5h/7d 规范字段
	if normalized := snapshot.Normalize(); normalized != nil {
		if normalized.Used5hPercent != nil {
			updates["codex_5h_used_percent"] = *normalized.Used5hPercent
		}
		if normalized.Reset5hSeconds != nil {
			updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
		}
		if normalized.Window5hMinutes != nil {
			updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
		}
		if normalized.Used7dPercent != nil {
			updates["codex_7d_used_percent"] = *normalized.Used7dPercent
		}
		if normalized.Reset7dSeconds != nil {
			updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
		}
		if normalized.Window7dMinutes != nil {
			updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
		}
		if reset5hAt := codexResetAtRFC3339(baseTime, normalized.Reset5hSeconds); reset5hAt != nil {
			updates["codex_5h_reset_at"] = *reset5hAt
		}
		if reset7dAt := codexResetAtRFC3339(baseTime, normalized.Reset7dSeconds); reset7dAt != nil {
			updates["codex_7d_reset_at"] = *reset7dAt
		}
	}

	return updates
}

// updateCodexUsageSnapshot saves the Codex usage snapshot to account's Extra field
// updateCodexUsageSnapshot 把 /responses 的 x-codex-* 全局头快照写入账号 codex_* Extra。
// ⚠️ 调用方必须排除 spark 影子账号(account.IsShadow()):影子的 codex_* 仅由 QueryUsage
// (/wham/usage bengalfox 道)更新,不能被全局头口径污染(外审第7轮 P1)。本函数仅持 accountID,
// 无法在此自检影子,故守卫前置到各调用点。
func (s *OpenAIGatewayService) updateCodexUsageSnapshot(ctx context.Context, accountID int64, snapshot *OpenAICodexUsageSnapshot) {
	if snapshot == nil {
		return
	}
	if s == nil || s.accountRepo == nil {
		return
	}

	now := time.Now()
	updates := buildCodexUsageExtraUpdates(snapshot, now)
	if len(updates) == 0 {
		return
	}
	if !s.getCodexSnapshotThrottle().Allow(accountID, now) {
		return
	}

	go func() {
		updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.accountRepo.UpdateExtra(updateCtx, accountID, updates)
	}()
}

func (s *OpenAIGatewayService) UpdateCodexUsageSnapshotFromHeaders(ctx context.Context, accountID int64, headers http.Header) {
	if accountID <= 0 || headers == nil {
		return
	}
	if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
		s.updateCodexUsageSnapshot(ctx, accountID, snapshot)
	}
}
