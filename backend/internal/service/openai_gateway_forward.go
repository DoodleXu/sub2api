package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Forward forwards request to OpenAI API
func (s *OpenAIGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	ctx, attempt, qualityErr := s.rateLimitService.BeginAccountQualityAttempt(ctx, account)
	if qualityErr != nil {
		return nil, qualityErr
	}
	defer attempt.Abort()
	if attempt != nil {
		bindAccountQualityAttemptContext(c, ctx)
	}
	qualityResult, qualityErr := s.forwardQualityCore(ctx, c, account, body)
	qualityErr = attempt.Finish(qualityResult, qualityErr)
	return qualityResult, qualityErr
}

// forwardQualityCore runs one OpenAI Responses forward attempt; Forward wraps it
// with the account quality / dispatch attempt bookkeeping.
func (s *OpenAIGatewayService) forwardQualityCore(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	beginUpstreamResponseModelObservation(c)
	clearGrokResponsesClientToolMapping(c)
	clearOpenAIResponsesClientToolMapping(c)
	clearOpenAIResponsesNamespaceNames(c)
	setCodexToolNameReverse(c, nil)
	rememberOpenAIResponsesClientInstructions(c, body)
	if _, err := s.prepareCodexAccountIdentitySource(ctx, c, account); err != nil {
		return nil, err
	}
	startTime := time.Now()
	canonicalImageIntentBody := body
	lineageGroupID := getOpenAIGroupIDFromContext(c)
	lineageEntryBody := body
	lineageSessionHash := ""
	// 常规请求没有失效密文记录时，不重复解析并哈希整份会话内容。
	// 首次恢复时仍从进场报文取键，避免兼容改写后与下一轮请求的键漂移。
	if store := s.getOpenAIWSStateStore(); store != nil && store.HasAnySessionInvalidEncryptedContent() {
		lineageSessionHash = s.openAIWSLineageSessionHashFromContext(c, lineageEntryBody)
		if invalid := store.GetSessionInvalidEncryptedContentDigests(lineageGroupID, lineageSessionHash); len(invalid) > 0 {
			body, _ = s.stripSessionInvalidEncryptedContentLogged(body, invalid, "http_invalid_encrypted_content_strip", account.ID, 0)
		}
	}

	restrictionResult := s.detectCodexClientRestriction(c, account, body)
	apiKeyID := getAPIKeyIDFromContext(c)
	// 执行作用域必须取自客户端原始身份：后面的账号 namespace 改写与指纹收敛会改掉
	// 请求体里的 client_metadata / prompt_cache_key，用改写后的值取键会让不同会话
	// 落到同一个键，也会与 WS 接入路径按原始报文算出的键对不上。
	wsExecutionScope, _ := resolveOpenAIWSExecutionScope(c, body, apiKeyID)
	logCodexCLIOnlyDetection(ctx, c, account, apiKeyID, restrictionResult, body)
	if restrictionResult.Enabled && !restrictionResult.Matched {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "forbidden_error",
				"message": CodexClientRestrictionMessage(restrictionResult),
			},
		})
		return nil, errors.New("codex_cli_only restriction: only codex official clients are allowed")
	}

	if account.IsOpenAI() && isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) {
		liteBody, changed, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(body, account)
		if liteErr != nil {
			param := "tools"
			var validationErr *openAIResponsesLiteValidationError
			if errors.As(liteErr, &validationErr) && strings.TrimSpace(validationErr.param) != "" {
				param = validationErr.param
			}
			setOpsUpstreamError(c, http.StatusBadRequest, liteErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": liteErr.Error(), "param": param}})
			return nil, liteErr
		}
		if changed {
			body = liteBody
		}
	}

	normalizedBody, normalized, err := normalizeOpenAICodexCompactReasoningEffortForAccount(c, account, body)
	if err != nil {
		return nil, err
	}
	if normalized {
		body = normalizedBody
	}
	if account.IsOpenAI() && !shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		replayedBody, replayChanged, replayErr := normalizeOpenAIResponsesReasoningContentReplay(body)
		if replayErr != nil {
			return nil, fmt.Errorf("normalize OpenAI reasoning content replay: %w", replayErr)
		}
		if replayChanged {
			body = replayedBody
		}
		compatBody, compatChanged, compatErr := normalizeOpenAIResponsesCompatibilityBodyWithOptions(body, account, openAIResponsesCompatibilityOptions{Config: s.cfg, ResponsesLite: isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) || isOpenAIResponsesLiteWebSocketPayload(body), Compact: isOpenAIResponsesCompactPath(c)})
		if compatErr != nil {
			return nil, fmt.Errorf("normalize OpenAI Responses compatibility body: %w", compatErr)
		}
		if compatChanged {
			body = compatBody
		}
		if account.IsOpenAIApiKey() && isOpenAIResponsesCompactPath(c) {
			compactReplayBody, compactReplayChanged, replayErr := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, true)
			if replayErr != nil {
				return nil, fmt.Errorf("normalize OpenAI compact stateless replay: %w", replayErr)
			}
			if compactReplayChanged {
				body = compactReplayBody
			}
		}
	}
	sanitizedToolBody, toolSchemaSanitized, toolSchemaErr := sanitizeOpenAIResponsesToolParameterTypes(body)
	if toolSchemaErr != nil {
		return nil, fmt.Errorf("sanitize OpenAI Responses tool parameters: %w", toolSchemaErr)
	}
	if toolSchemaSanitized {
		body = sanitizedToolBody
	}
	wsDecision := s.getOpenAIWSProtocolResolver().Resolve(account)
	// 仅允许 WS 入站请求走 WS 上游，避免出现 HTTP -> WS 协议混用。
	wsDecision = resolveOpenAIWSDecisionByClientTransport(wsDecision, GetOpenAIClientTransport(c))
	passthroughEnabled := account.IsOpenAIPassthroughEnabled()
	compactPath := isOpenAIResponsesCompactPath(c)
	if shouldFlattenOpenAIResponsesNamespaces(account, wsDecision.Transport, passthroughEnabled, compactPath) {
		body, err = flattenOpenAIResponsesNamespaces(c, body)
		if err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": "invalid_request_error", "message": err.Error(), "param": "tools",
			}})
			return nil, err
		}
	}
	if shouldStripOpenAIResponsesInputNamespaces(account, wsDecision.Transport, passthroughEnabled) {
		keepToolCallNamespaces := shouldKeepOpenAIResponsesToolCallNamespaces(account, wsDecision.Transport, passthroughEnabled, compactPath, body)
		body, err = stripOpenAIResponsesInputNamespaces(body, keepToolCallNamespaces)
		if err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": "invalid_request_error", "message": err.Error(), "param": "input",
			}})
			return nil, err
		}
	}
	if !passthroughEnabled {
		normalizedJSONBody, changed, normalizeErr := ensureResponsesJSONModeInputInstructionInBody(body)
		if normalizeErr != nil {
			return nil, fmt.Errorf("normalize responses JSON mode input: %w", normalizeErr)
		}
		if changed {
			body = normalizedJSONBody
		}
	}

	originalBody := body
	requestView := newOpenAIRequestView(body)
	reqModel, reqStream, promptCacheKey := requestView.Model, requestView.Stream, requestView.PromptCacheKey
	originalModel := reqModel
	nativeDeepSeekResponses := account.UsesNativeCNResponses()

	if account.Platform == PlatformGrok {
		return s.forwardGrokResponses(ctx, c, account, body, originalModel, reqStream, startTime)
	}

	// 上游协议统一由 resolveUpstreamProtocol 判定（按模型分流时带上游模型目录）。OpenAI API Key 账号只会落到
	// Responses / Chat Completions，上面的归一化对两条路径都生效。
	routingModel := upstreamRoutingModel(account, body, "")
	if account.IsOpenCodeGo() && IsOpenCodeUnsupportedModel(routingModel) {
		return nil, writeOpenCodeUnsupportedModelError(c, false, routingModel)
	}
	switch s.resolveUpstreamProtocolFor(ctx, account, APIProtocolResponses, routingModel) {
	case APIProtocolAnthropic:
		// Responses 客户端 × Anthropic 上游：转成 Anthropic 请求走原生端点。不能落到
		// raw-CC 分支——其 URL 构造会把 anthropic base 当 CC base 用。
		// 账号映射未命中时以去除首尾空白的请求模型兜底，计费名与上游模型名一致。
		return s.forwardResponsesViaNativeAnthropic(ctx, c, account, body, reqModel)
	case APIProtocolChatCompletions:
		return s.forwardResponsesViaRawChatCompletions(ctx, c, account, body, startTime)
	}
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey && !shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		sanitizedBody, changed, sanitizeErr := sanitizeOpenAIResponsesInputItemIDs(body)
		if sanitizeErr != nil {
			return nil, fmt.Errorf("sanitize OpenAI Responses input item IDs: %w", sanitizeErr)
		}
		if changed {
			body = sanitizedBody
			originalBody = sanitizedBody
			requestView = newOpenAIRequestView(sanitizedBody)
			reqModel, reqStream, promptCacheKey = requestView.Model, requestView.Stream, requestView.PromptCacheKey
			originalModel = reqModel
		}
	}

	compatMessagesBridge := isOpenAICompatMessagesBridgeBody(body)
	setOpenAICompatMessagesBridgeContext(c, compatMessagesBridge)

	isCodexCLI := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)
	codexImageGenerationExplicitToolPolicy := codexImageGenerationExplicitToolPolicyAllow
	if isCodexCLI {
		codexImageGenerationExplicitToolPolicy = account.CodexImageGenerationExplicitToolPolicy()
	}
	if c != nil {
		c.Set("openai_ws_transport_decision", string(wsDecision.Transport))
		c.Set("openai_ws_transport_reason", wsDecision.Reason)
	}
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
		logOpenAIWSModeDebug(
			"selected account_id=%d account_type=%s transport=%s reason=%s model=%s stream=%v",
			account.ID,
			account.Type,
			normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
			normalizeOpenAIWSLogValue(wsDecision.Reason),
			reqModel,
			reqStream,
		)
	}
	// 当前仅支持 WSv2；WSv1 命中时直接返回错误，避免出现“配置可开但行为不确定”。
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocket {
		if c != nil {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "OpenAI WSv1 is temporarily unsupported. Please enable responses_websockets_v2.",
				},
			})
		}
		return nil, errors.New("openai ws v1 is temporarily unsupported; use ws v2")
	}
	if passthroughEnabled {
		attemptImageIntentInvalidated := false
		if isCodexCLI && codexImageGenerationExplicitToolPolicy == codexImageGenerationExplicitToolPolicyStrip {
			strippedBody, changed, stripErr := stripOpenAIImageGenerationToolsFromRawPayload(body)
			if stripErr != nil {
				return nil, stripErr
			}
			if changed {
				body = strippedBody
				originalBody = strippedBody
				attemptImageIntentInvalidated = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Stripped /responses image_generation tool for Codex client by account policy")
			}
		}
		// 透传分支只需要轻量提取字段，避免热路径全量 Unmarshal。
		mappedModel := account.GetMappedModel(reqModel)
		reasoningEffort := extractOpenAIReasoningEffortFromBody(body, mappedModel)
		// 国产模型默认 effort 补充：也要用 mappedModel 判定是否是 passback-required 上游。
		reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, mappedModel)
		return s.forwardOpenAIPassthrough(ctx, c, account, originalBody, canonicalImageIntentBody, reqModel, attemptImageIntentInvalidated, reasoningEffort, reqStream, startTime)
	}

	bodyModified := false
	clientPromptCacheKey := promptCacheKey
	var reqBody map[string]any
	// Each patched body supersedes the previous one, so this attempt holds only
	// the live output's budget, returned when Forward ends rather than retained
	// across failover attempts until the handler returns.
	releasePatchedBodyMemory := func() {}
	defer func() { releasePatchedBodyMemory() }()
	flushPatches := func() error {
		if !requestView.HasPatches() {
			return nil
		}
		patchCtx, releasePatch := httputil.WithRequestBodyMemoryScope(ctx)
		patchedBody, patchErr := requestView.ApplyPatches(patchCtx)
		if patchErr == nil {
			releasePatchedBodyMemory()
			releasePatchedBodyMemory = releasePatch
			body = patchedBody
			requestView = newOpenAIRequestView(body)
			reqBody = nil
			bodyModified = false
			return nil
		}
		releasePatch()
		if errors.Is(patchErr, httputil.ErrRequestBodyMemoryBudget) {
			return patchErr
		}
		// Raw edits cannot express this patch set. Replay it on the decoded
		// body so the map-based fallback still applies every edit.
		if reqBody == nil {
			decoded, decodeErr := requestView.Decode(c)
			if decodeErr != nil {
				return decodeErr
			}
			reqBody = decoded
		}
		requestView.MovePatchesTo(reqBody)
		return nil
	}
	ensureReqBody := func() (map[string]any, error) {
		if err := flushPatches(); err != nil {
			return nil, err
		}
		if reqBody != nil {
			return reqBody, nil
		}
		decoded, decodeErr := requestView.Decode(c)
		if decodeErr != nil {
			return nil, decodeErr
		}
		reqBody = decoded
		return reqBody, nil
	}
	markPatchSet := func(path string, value any) {
		bodyModified = true
		if requestView.patchesDisabled {
			if reqBody != nil {
				setOpenAIRequestMapPath(reqBody, path, value)
			}
			return
		}
		requestView.MarkPatchSet(path, value)
	}
	markPatchDelete := func(path string) {
		bodyModified = true
		if requestView.patchesDisabled {
			if reqBody != nil {
				deleteOpenAIRequestMapPath(reqBody, path)
			}
			return
		}
		requestView.MarkPatchDelete(path)
	}
	disablePatch := func() {
		requestView.DisablePatches()
	}
	markDecodedModified := func() {
		bodyModified = true
		disablePatch()
	}

	apiKey := getAPIKeyFromContext(c)
	imageGenerationAllowed := GroupAllowsImageGeneration(nil)
	if apiKey != nil {
		imageGenerationAllowed = GroupAllowsImageGeneration(apiKey.Group)
	}
	codexImageGenerationBridgeEnabled := isCodexCLI &&
		!isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) &&
		imageGenerationAllowed &&
		codexImageGenerationExplicitToolPolicy != codexImageGenerationExplicitToolPolicyStrip &&
		s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey)
	codexImageGenerationBridgeShouldApply := codexImageGenerationBridgeEnabled
	var imageIntent bool
	canonicalImageIntent := resolveOpenAIImageIntentHint(c, reqModel, canonicalImageIntentBody, IsImageGenerationIntent)
	if isCodexCLI && codexImageGenerationExplicitToolPolicy == codexImageGenerationExplicitToolPolicyStrip {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if stripOpenAIImageGenerationTools(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Stripped /responses image_generation tool for Codex client by account policy")
		}
		imageIntent = IsImageGenerationIntentMap(openAIResponsesEndpoint, reqModel, decoded)
	} else {
		imageIntent = canonicalImageIntent
	}
	codexBridgeFallbackEligible := codexImageGenerationBridgeShouldApply && !imageIntent
	codexBridgeInjectedForText := false
	codexBridgeSetToolChoiceAuto := false
	if imageIntent && !imageGenerationAllowed {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": ImageGenerationPermissionMessage()}})
		return nil, errors.New("image generation disabled for group")
	}

	billingModel := account.GetMappedModel(reqModel)
	if billingModel != reqModel {
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Model mapping applied: %s -> %s (account: %s, isCodexCLI: %v)", reqModel, billingModel, account.Name, isCodexCLI)
		reqModel = billingModel
		markPatchSet("model", billingModel)
	}
	upstreamModel := billingModel
	isCompactRequest := isOpenAIResponsesCompactPath(c)
	compactMapped := false
	if isCompactRequest {
		compactMappedModel := s.resolveOpenAICompactFallbackModel(account, originalModel)
		if compactMappedModel != "" && compactMappedModel != billingModel {
			compactMapped = true
			upstreamModel = compactMappedModel
			reqModel = compactMappedModel
			markPatchSet("model", compactMappedModel)
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Compact model mapping applied: %s -> %s (account: %s, isCodexCLI: %v)", billingModel, compactMappedModel, account.Name, isCodexCLI)
		}
	}
	instructions := gjson.GetBytes(body, "instructions")
	instructionsEmpty := !instructions.Exists() || instructions.Type != gjson.String || strings.TrimSpace(instructions.String()) == ""
	if instructionsEmpty && account.UsesOpenAICodexProtocol() && !compatMessagesBridge && !nativeDeepSeekResponses {
		markPatchSet("instructions", defaultCodexSynthInstructions(upstreamModel))
	}
	if !compactMapped {
		modelForNormalize := reqModel
		if modelForNormalize == "" {
			modelForNormalize = requestView.Model
		}
		upstreamModel = normalizeOpenAIModelForUpstream(account, modelForNormalize)
		if upstreamModel != "" && upstreamModel != modelForNormalize {
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Upstream model resolved: %s -> %s (account: %s, type: %s, isCodexCLI: %v)", modelForNormalize, upstreamModel, account.Name, account.Type, isCodexCLI)
			reqModel = upstreamModel
			markPatchSet("model", upstreamModel)
		}
	}
	SetOpsUpstreamModel(c, upstreamModel)
	if strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()) == "minimal" {
		markPatchSet("reasoning.effort", "none")
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized reasoning.effort: minimal -> none (account: %s)", account.Name)
	}

	imageIntent = imageIntent || IsImageGenerationIntent(openAIResponsesEndpoint, reqModel, nil) || isOpenAIImageGenerationModel(upstreamModel)
	if imageIntent && !imageGenerationAllowed {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": ImageGenerationPermissionMessage()}})
		return nil, errors.New("image generation disabled for group")
	}

	sparkImageToolRequest := isCodexSparkModel(upstreamModel) && openAIRequestBodyHasImageGenerationTool(body)
	// /responses/compact 是会话压缩请求：上游不接受 tool_choice（400 unknown_parameter），
	// 注入 image_generation 工具也没有意义，整块豁免。
	if imageGenerationAllowed && !isCompactRequest && (codexImageGenerationBridgeShouldApply || isOpenAIImageGenerationModel(requestView.Model) || openAIRequestBodyImageGenerationToolNeedsNormalization(body) || isOpenAIImageGenerationModel(upstreamModel) || sparkImageToolRequest) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if codexImageGenerationBridgeShouldApply {
			bridgeMutation := applyCodexImageGenerationBridge(decoded)
			if bridgeMutation.Modified {
				markDecodedModified()
			}
			if bridgeMutation.ToolInjected {
				codexBridgeInjectedForText = codexBridgeFallbackEligible
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Injected /responses image_generation tool for Codex client")
			}
			if bridgeMutation.ToolChoiceAuto {
				codexBridgeSetToolChoiceAuto = codexBridgeFallbackEligible
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Set /responses image_generation tool_choice=auto for Codex client")
			}
			if bridgeMutation.InstructionsAdded {
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Added Codex image_generation bridge instructions")
			}
		}
		if normalizeOpenAIResponsesImageGenerationTools(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized /responses image_generation tool payload")
		}
		if (isCodexSparkModel(upstreamModel) || isCodexSparkModel(firstNonEmptyString(decoded["model"]))) && stripCodexSparkImageGenerationTools(decoded) {
			markDecodedModified()
		}
		if normalizeOpenAIResponsesImageOnlyModel(decoded) {
			markDecodedModified()
			if model, ok := decoded["model"].(string); ok {
				upstreamModel = strings.TrimSpace(model)
			}
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized /responses image-only model request inbound_model=%s image_model=%s upstream_model=%s", requestView.Model, billingModel, upstreamModel)
		}
		if err := validateOpenAIResponsesImageModel(decoded, upstreamModel); err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error(), "param": "model"}})
			return nil, err
		}
		if hasOpenAIImageGenerationTool(decoded) {
			imageIntent = true
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s account_type=%s", requestView.Model, upstreamModel, account.Type)
		}
	} else if imageGenerationAllowed && imageIntent && openAIRequestBodyHasImageGenerationTool(body) {
		// 完整 image_generation tool 只做 raw 计费读取，校验/桥接/旧字段迁移命中时才展开大 input map。
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s account_type=%s", requestView.Model, upstreamModel, account.Type)
	}

	if isCodexSparkModel(upstreamModel) && openAIRequestBodyMayContainImageInput(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err := validateCodexSparkInput(decoded, upstreamModel); err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error(), "param": "input"}})
			return nil, err
		}
	}

	if account.UsesOpenAICodexProtocol() {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		codexResult := codexTransformResult{}
		if compatMessagesBridge {
			codexResult = applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{
				IsCodexCLI:                          isCodexCLI,
				IsCompact:                           isCompactRequest,
				SkipDefaultInstructions:             true,
				PreserveToolCallIDs:                 true,
				OmitPromotedSystemMessagesFromInput: true,
				ResponsesLite:                       isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) || isOpenAIResponsesLiteWebSocketPayload(body),
			})
			ensureCodexOAuthInstructionsField(decoded)
			markDecodedModified()
		} else {
			codexResult = applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{
				IsCodexCLI:                          isCodexCLI,
				IsCompact:                           isCompactRequest,
				OmitPromotedSystemMessagesFromInput: !compatMessagesBridge,
				ResponsesLite:                       isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) || isOpenAIResponsesLiteWebSocketPayload(body),
			})
		}
		if codexResult.Modified {
			markDecodedModified()
		}
		// 带真实 device_id 时补齐 client_metadata 安装标识，与真实 Codex 对齐（compact 形态不同，跳过）。
		if !isCompactRequest && applyCodexClientMetadata(decoded, account) {
			markDecodedModified()
		}
		if currentClientPromptCacheKey, ok := decoded["prompt_cache_key"].(string); ok {
			clientPromptCacheKey = currentClientPromptCacheKey
		}
		// Account namespace is orthogonal to fingerprint convergence: preserve
		// each client's identity cardinality, but never reuse it across OAuth
		// credentials after scheduler failover.
		if !isCompactRequest && applyCodexAccountIdentityClientMetadataMap(decoded, codexAccountIdentitySource(c, account), getAPIKeyIDFromContext(c), codexClientSessionID(c)) {
			markDecodedModified()
		}
		stageCodexFingerprintIDs(c, nil)
		// 指纹收敛：一次性解析收敛 ID，请求体和出站头共享同一份 IDs（保证 turn_id 等随机字段一致）。
		// fingerprintIDs 在此处解析，后续 buildUpstreamRequest 中使用同一份。
		if !isCompactRequest {
			var clientHeaders http.Header
			if c != nil && c.Request != nil {
				clientHeaders = c.Request.Header
			}
			fpIDs := resolveCodexFingerprintIDsFromRequest(account, clientHeaders)
			if fpIDs != nil {
				if applyCodexFingerprintClientMetadata(decoded, fpIDs) {
					markDecodedModified()
				}
			}
			// 将 fpIDs 存入 gin context，供 buildUpstreamRequest 中头改写使用。
			// 无条件覆写（含 nil）：failover 从收敛账号切到 off 账号时，上一
			// 账号的 IDs 不得残留（stageCodexFingerprintIDs 注释）。
			stageCodexFingerprintIDs(c, fpIDs)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if strings.TrimSpace(clientPromptCacheKey) != "" {
			// The body now carries an account-scoped value. Keep the original here
			// so the header builder derives the same namespace exactly once.
			promptCacheKey = clientPromptCacheKey
		} else if currentPromptCacheKey, ok := decoded["prompt_cache_key"].(string); ok && currentPromptCacheKey != "" {
			// Fingerprint convergence may inject a default key when the client did
			// not provide one; preserve that existing fallback.
			promptCacheKey = currentPromptCacheKey
		} else if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
	}

	if !SupportsVerbosity(upstreamModel) && gjson.GetBytes(body, "text.verbosity").Exists() {
		markPatchDelete("text.verbosity")
	}

	if !isCodexCLI {
		maxOutputTokens := gjson.GetBytes(body, "max_output_tokens")
		if maxOutputTokens.Exists() {
			switch account.Platform {
			case PlatformOpenAI, PlatformDeepseek:
				// Responses-native output limit; keep until an upstream explicitly rejects it.
			case PlatformAnthropic:
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				delete(decoded, "max_output_tokens")
				if _, hasMaxTokens := decoded["max_tokens"]; !hasMaxTokens {
					decoded["max_tokens"] = maxOutputTokens.Value()
				}
				markDecodedModified()
			case PlatformGemini:
				markPatchDelete("max_output_tokens")
			default:
				markPatchDelete("max_output_tokens")
			}
		}
		if account.Platform == PlatformOpenAI {
			if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
				if !gjson.GetBytes(body, "max_output_tokens").Exists() {
					markPatchSet("max_output_tokens", maxTokens.Value())
				}
				markPatchDelete("max_tokens")
			}
		}
		if gjson.GetBytes(body, "max_completion_tokens").Exists() && (account.Type == AccountTypeAPIKey || account.Platform != PlatformOpenAI) {
			markPatchDelete("max_completion_tokens")
		}
		for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier", "prompt_cache_options"} {
			if gjson.GetBytes(body, unsupportedField).Exists() {
				markPatchDelete(unsupportedField)
			}
		}
	}
	if account.UsesOpenAICodexProtocol() && wsDecision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 && gjson.GetBytes(body, "previous_response_id").Exists() {
		markPatchDelete("previous_response_id")
	}
	if openAIRequestBodyMayContainEmptyBase64InputImage(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if sanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(decoded) {
			markDecodedModified()
		}
	}
	if openAIResponsesInputArgumentsMayNeedNormalize(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if normalizeOpenAIResponsesInputArguments(decoded) {
			markDecodedModified()
		}
	}

	rawTier := requestView.ServiceTier
	if forcedTier := openAIGroupForcedServiceTier(ctx, account, upstreamModel); forcedTier != "" {
		rawTier = forcedTier
		if requestView.ServiceTier != forcedTier {
			markPatchSet("service_tier", forcedTier)
		}
	}
	if rawTier != "" {
		if normTier := normalizedOpenAIServiceTierValue(rawTier); normTier != "" {
			action, errMsg := s.evaluateOpenAIFastPolicy(ctx, account, upstreamModel, normTier)
			switch action {
			case BetaPolicyActionBlock:
				msg := errMsg
				if msg == "" {
					msg = fmt.Sprintf("openai service_tier=%s is not allowed for model %s", normTier, upstreamModel)
				}
				blocked := &OpenAIFastBlockedError{Message: msg}
				writeOpenAIFastPolicyBlockedResponse(c, blocked)
				return nil, blocked
			case BetaPolicyActionFilter:
				markPatchDelete("service_tier")
			case OpenAIFastPolicyActionForcePriority:
				if rawTier != OpenAIFastTierPriority {
					markPatchSet("service_tier", OpenAIFastTierPriority)
				}
			default:
				if normTier != rawTier {
					markPatchSet("service_tier", normTier)
				}
			}
		}
	} else if s.shouldForceOpenAIFastPriorityForMissingTier(ctx, account, upstreamModel) {
		markPatchSet("service_tier", OpenAIFastTierPriority)
	}

	if bodyModified {
		if err := flushPatches(); err != nil {
			return nil, err
		}
		if bodyModified {
			decoded, decodeErr := ensureReqBody()
			if decodeErr != nil {
				return nil, decodeErr
			}
			var marshalErr error
			body, marshalErr = marshalOpenAIUpstreamJSON(decoded)
			if marshalErr != nil {
				return nil, fmt.Errorf("serialize request body: %w", marshalErr)
			}
			requestView = newOpenAIRequestView(body)
		}
	}
	if account.UsesOpenAICodexProtocol() && !gjson.GetBytes(body, "previous_response_id").Exists() {
		var requestBody map[string]any
		if decodeErr := decodeOpenAIJSONUseNumber(body, &requestBody); decodeErr != nil {
			return nil, decodeErr
		}
		if input, ok := requestBody["input"].([]any); ok && sanitizeOpenAIResponsesOrphanToolOutputs(requestBody, input, false) {
			var marshalErr error
			body, marshalErr = marshalOpenAIUpstreamJSON(requestBody)
			if marshalErr != nil {
				return nil, fmt.Errorf("serialize orphan-cleaned request body: %w", marshalErr)
			}
		}
	}

	// Function-style upstreams cannot consume Responses custom tools. Keep the
	// request/response mapping on the native path as well as passthrough so
	// DeepSeek API-key accounts preserve the fork's client-tool contract.
	if account != nil && account.Type == AccountTypeAPIKey &&
		(account.Platform == PlatformOpenAI || account.Platform == PlatformDeepseek) &&
		!isOpenAIResponsesCompactPath(c) && needsOpenAIResponsesClientToolAdaptation(body) {
		adaptedBody, mapping, adaptErr := adaptOpenAIResponsesClientTools(body)
		if adaptErr != nil {
			return nil, adaptErr
		}
		body = adaptedBody
		c.Set(openAIResponsesClientToolMappingContextKey, mapping)
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfg OpenAIResponsesImageBillingConfig
		var imageCfgErr error
		if reqBody != nil {
			imageCfg, imageCfgErr = resolveOpenAIResponsesImageBillingConfigDetailed(reqBody, billingModel)
		} else {
			imageCfg, imageCfgErr = resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, billingModel)
		}
		if imageCfgErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, imageCfgErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": imageCfgErr.Error(), "param": "size"}})
			return nil, imageCfgErr
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}
	// 图片意图必须基于账号映射、Codex bridge 注入和工具归一化完成后的最终
	// payload 判断。此处位于任何上游连接之前，并让 release 覆盖完整响应周期。
	var imageGenerationRelease func()
	if imageIntent {
		imageGenerationRelease, err = acquireOpenAIImageGenerationSlot(ctx)
		if err != nil {
			return nil, err
		}
		if imageGenerationRelease != nil {
			defer imageGenerationRelease()
		}
	}

	// Get access token
	identityMetadata, identityErr := s.agentIdentityRequestMetadata(ctx, account)
	if identityErr != nil {
		return nil, fmt.Errorf("resolve agent identity request metadata: %w", identityErr)
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 命中 WS 时仅走 WebSocket Mode；不再自动回退 HTTP。
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
		// WS 分支需要结构化 payload 与重连恢复，命中后再触发 full-map decode。
		wsReqBody, err := ensureReqBody()
		if err != nil {
			return nil, err
		}
		_, hasPreviousResponseID := wsReqBody["previous_response_id"]
		logOpenAIWSModeDebug(
			"forward_start account_id=%d account_type=%s model=%s stream=%v has_previous_response_id=%v",
			account.ID,
			account.Type,
			upstreamModel,
			reqStream,
			hasPreviousResponseID,
		)
		maxAttempts := 2 // initial request plus one proven protocol repair
		wsAttempts := 0
		var wsResult *OpenAIForwardResult
		var wsErr error
		wsLastFailureReason := ""
		wsPrevResponseRecoveryTried := false
		wsInvalidEncryptedContentRecoveryTried := false
		recoverPrevResponseNotFound := func(attempt int) bool {
			if wsPrevResponseRecoveryTried {
				return false
			}
			previousResponseID := openAIWSPayloadString(wsReqBody, "previous_response_id")
			if previousResponseID == "" {
				logOpenAIWSModeInfo(
					"reconnect_prev_response_recovery_skip account_id=%d attempt=%d reason=missing_previous_response_id previous_response_id_present=false",
					account.ID,
					attempt,
				)
				return false
			}
			if HasFunctionCallOutput(wsReqBody) {
				logOpenAIWSModeInfo(
					"reconnect_prev_response_recovery_skip account_id=%d attempt=%d reason=has_function_call_output previous_response_id_present=true",
					account.ID,
					attempt,
				)
				return false
			}
			delete(wsReqBody, "previous_response_id")
			wsPrevResponseRecoveryTried = true
			logOpenAIWSModeInfo(
				"reconnect_prev_response_recovery account_id=%d attempt=%d action=drop_previous_response_id retry=1 previous_response_id=%s previous_response_id_kind=%s",
				account.ID,
				attempt,
				truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(ClassifyOpenAIPreviousResponseIDKind(previousResponseID)),
			)
			return true
		}
		recoverInvalidEncryptedContent := func(attempt int) bool {
			if wsInvalidEncryptedContentRecoveryTried {
				return false
			}
			removedReasoningItems := trimOpenAIEncryptedReasoningItems(wsReqBody)
			if !removedReasoningItems {
				logOpenAIWSModeInfo(
					"reconnect_invalid_encrypted_content_recovery_skip account_id=%d attempt=%d reason=missing_encrypted_reasoning_items",
					account.ID,
					attempt,
				)
				return false
			}
			previousResponseID := openAIWSPayloadString(wsReqBody, "previous_response_id")
			hasFunctionCallOutput := HasFunctionCallOutput(wsReqBody)
			if previousResponseID != "" && !hasFunctionCallOutput {
				delete(wsReqBody, "previous_response_id")
			}
			wsInvalidEncryptedContentRecoveryTried = true
			logOpenAIWSModeInfo(
				"reconnect_invalid_encrypted_content_recovery account_id=%d attempt=%d action=drop_encrypted_reasoning_items retry=1 previous_response_id_present=%v previous_response_id=%s previous_response_id_kind=%s has_function_call_output=%v dropped_previous_response_id=%v",
				account.ID,
				attempt,
				previousResponseID != "",
				truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(ClassifyOpenAIPreviousResponseIDKind(previousResponseID)),
				hasFunctionCallOutput,
				previousResponseID != "" && !hasFunctionCallOutput,
			)
			return true
		}
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			wsAttempts = attempt
			wsResult, wsErr = s.forwardOpenAIWSV2(
				ctx,
				c,
				account,
				wsReqBody,
				clientPromptCacheKey,
				wsExecutionScope,
				token,
				wsDecision,
				isCodexCLI,
				reqStream,
				originalModel,
				upstreamModel,
				startTime,
				attempt,
				wsLastFailureReason,
			)
			if wsErr == nil {
				break
			}
			if c != nil && c.Writer != nil && c.Writer.Written() {
				break
			}

			reason, retryable := classifyOpenAIWSReconnectReason(wsErr)
			if reason != "" {
				wsLastFailureReason = reason
			}
			// previous_response_not_found 说明续链锚点不可用：
			// 对非 function_call_output 场景，允许一次“去掉 previous_response_id 后重放”。
			if attempt == 1 && reason == "previous_response_not_found" && recoverPrevResponseNotFound(attempt) && AllowAccountProtocolRepair(ctx) {
				continue
			}
			if attempt == 1 && reason == "invalid_encrypted_content" && recoverInvalidEncryptedContent(attempt) && AllowAccountProtocolRepair(ctx) {
				continue
			}
			if retryable {
				s.recordOpenAIWSRetryExhausted()
				logOpenAIWSModeInfo(
					"reconnect_exhausted account_id=%d attempts=%d max_retries=%d reason=%s",
					account.ID,
					attempt,
					openAIWSReconnectRetryLimit,
					normalizeOpenAIWSLogValue(reason),
				)
			} else if reason != "" {
				s.recordOpenAIWSNonRetryableFastFallback()
				logOpenAIWSModeInfo(
					"reconnect_stop account_id=%d attempt=%d reason=%s",
					account.ID,
					attempt,
					normalizeOpenAIWSLogValue(reason),
				)
			}
			break
		}
		if wsErr == nil {
			firstTokenMs := int64(0)
			hasFirstTokenMs := wsResult != nil && wsResult.FirstTokenMs != nil
			if hasFirstTokenMs {
				firstTokenMs = int64(*wsResult.FirstTokenMs)
			}
			requestID := ""
			if wsResult != nil {
				requestID = strings.TrimSpace(wsResult.RequestID)
			}
			logOpenAIWSModeDebug(
				"forward_succeeded account_id=%d request_id=%s stream=%v has_first_token_ms=%v first_token_ms=%d ws_attempts=%d",
				account.ID,
				requestID,
				reqStream,
				hasFirstTokenMs,
				firstTokenMs,
				wsAttempts,
			)
			wsResult.UpstreamModel = upstreamModel
			if wsResult.BillingModel == "" {
				wsResult.BillingModel = billingModel
			}
			if wsResult.ImageCount > 0 {
				wsResult.ImageSize = imageSizeTier
				wsResult.ImageInputSize = imageInputSize
				wsResult.BillingModel = imageBillingModel
			}
			return wsResult, nil
		}
		if c == nil || c.Writer == nil || !c.Writer.Written() {
			var failure *UpstreamFailoverError
			if errors.As(wsErr, &failure) {
				return nil, wsErr
			}
			if _, retryable := classifyOpenAIWSReconnectReason(wsErr); retryable {
				return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Reason: "ws_transport_failure"}
			}
		}
		s.writeOpenAIWSFallbackErrorResponse(c, account, wsErr)
		return nil, wsErr
	}

	httpInvalidEncryptedContentRetryTried := false
	httpCodexImageBridgeFallbackTried := false
	agentTaskRecoveryTried := false
	rejectedFieldRetryState := newOpenAIResponsesRejectedFieldRetryState(body)
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, reqModel)
	reasoningEffortValue := ""
	if reasoningEffort != nil {
		reasoningEffortValue = *reasoningEffort
	}
	firstOutputTimeout := time.Duration(0)
	if reqStream && account.Platform == PlatformOpenAI {
		firstOutputTimeout = s.openAIFirstOutputTimeout(reasoningEffortValue)
	}
	compactFallbackRetried := false
	for {
		// Build upstream request
		upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
		var headerGuard *openAIFirstOutputHeaderGuard
		if firstOutputTimeout > 0 {
			upstreamCtx, headerGuard = newOpenAIFirstOutputHeaderGuard(upstreamCtx, releaseUpstreamCtx, startTime.Add(firstOutputTimeout))
		}
		syncOpenAIResponsesInstructionsEcho(c, body)
		upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, body, token, reqStream, promptCacheKey, isCodexCLI)
		if headerGuard == nil {
			releaseUpstreamCtx()
		}
		if err != nil {
			if headerGuard != nil {
				headerGuard.close()
			}
			return nil, err
		}
		identityMetadata.bindAuthorization(upstreamReq.Header.Get("Authorization"))
		c.Request = c.Request.WithContext(withAgentIdentityRequestRedactor(c.Request.Context(), identityMetadata.redactor))

		// Get proxy URL
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}

		// Send request
		upstreamStart := time.Now()
		resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		if headerGuard != nil && headerGuard.stopHeaderWait() {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			headerGuard.close()
			return nil, s.newOpenAIFirstOutputTimeoutError(ctx, c, account, nil, "", startTime, originalModel, reasoningEffortValue, firstOutputTimeout, "response_headers", nil)
		}
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if headerGuard != nil {
				headerGuard.close()
			}
			// Transport-level failure (proxy/DNS/TCP/TLS — no HTTP response). Convert to
			// a failover so the handler switches to a healthy account, and temporarily
			// unschedule the account on durable faults (e.g. rejected proxy credentials).
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
		}
		if headerGuard != nil {
			resp.Body = &openAIRequestContextReadCloser{ReadCloser: resp.Body, cleanup: headerGuard.close}
		}

		// Handle error response
		if resp.StatusCode >= 400 {
			respBody := s.readUpstreamErrorBody(resp)
			if failure := s.rateLimitService.BalanceFailover(ctx, c, account, resp.StatusCode, respBody); failure != nil {
				return nil, failure
			}
			respBody = identityMetadata.redactor(respBody)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
			upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
			upstreamCode := extractUpstreamErrorCode(respBody)
			if retryBody, fallbackModel, retry := s.prepareOpenAICompactFallbackRetry(c, account, originalModel, body, resp.StatusCode, upstreamMsg, respBody, compactFallbackRetried); retry {
				compactFallbackRetried = true
				s.appendOpenAICompactFallbackRetryOps(c, account, resp, respBody, upstreamMsg, false)
				body = retryBody
				upstreamModel = fallbackModel
				requestView = newOpenAIRequestView(body)
				reqBody = nil
				continue
			}
			if !agentTaskRecoveryTried && identityMetadata.isAgentIdentity && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
				agentTaskRecoveryTried = true
				if err := s.recoverAgentIdentityTask(ctx, account, identityMetadata.taskIDUsed); err != nil {
					return nil, fmt.Errorf("agent identity task recovery failed: %w", err)
				}
				refreshedMetadata, metadataErr := s.agentIdentityRequestMetadata(ctx, account)
				if metadataErr != nil {
					return nil, fmt.Errorf("refresh agent identity request metadata: %w", metadataErr)
				}
				identityMetadata = refreshedMetadata
				continue
			}
			if !httpCodexImageBridgeFallbackTried && codexBridgeInjectedForText && isOpenAIImageGenerationDisabledUpstreamError(resp.StatusCode, upstreamMsg, respBody) {
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				if stripCodexImageGenerationBridgeArtifacts(decoded, codexBridgeSetToolChoiceAuto) {
					body, err = marshalOpenAIUpstreamJSON(decoded)
					if err != nil {
						return nil, fmt.Errorf("serialize codex image bridge fallback body: %w", err)
					}
					requestView = newOpenAIRequestView(body)
					reqBody = nil
					bodyModified = false
					httpCodexImageBridgeFallbackTried = true
					logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Retrying request without auto-injected Codex image_generation bridge after upstream permission_error (account: %s)", account.Name)
					continue
				}
				httpCodexImageBridgeFallbackTried = true
			}
			invalidEncryptedContentError := upstreamCode == "invalid_encrypted_content" ||
				(upstreamCode == "thinking_signature_invalid" &&
					strings.Contains(upstreamMsg, "The encrypted content") &&
					strings.Contains(upstreamMsg, "could not be verified") &&
					strings.Contains(upstreamMsg, "could not be decrypted or parsed"))
			if !httpInvalidEncryptedContentRetryTried && resp.StatusCode == http.StatusBadRequest && invalidEncryptedContentError {
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				invalidDigests := collectOpenAIEncryptedContentDigestsRaw(body)
				if trimOpenAIEncryptedReasoningItems(decoded) {
					if lineageSessionHash == "" {
						lineageSessionHash = s.openAIWSLineageSessionHashFromContext(c, lineageEntryBody)
					}
					s.markOpenAIWSInvalidEncryptedContentLineage(lineageGroupID, lineageSessionHash, invalidDigests)
					body, err = marshalOpenAIUpstreamJSON(decoded)
					if err != nil {
						return nil, fmt.Errorf("serialize invalid_encrypted_content retry body: %w", err)
					}
					httpInvalidEncryptedContentRetryTried = true
					rejectedFieldRetryState.remember(body)
					logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Retrying non-WSv2 request once after invalid_encrypted_content (account: %s)", account.Name)
					continue
				}
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Skip non-WSv2 invalid_encrypted_content retry because encrypted reasoning items are missing (account: %s)", account.Name)
			}
			if retryBody, reason, changed, retryErr := normalizeOpenAIResponsesRejectedFieldRetryBody(resp.StatusCode, body, respBody); retryErr != nil {
				return nil, fmt.Errorf("normalize rejected Responses field retry body: %w", retryErr)
			} else if changed && rejectedFieldRetryState.Allow(retryBody) && AllowAccountProtocolRepair(ctx) {
				body = retryBody
				requestView = newOpenAIRequestView(body)
				reqBody = nil
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Retrying non-WSv2 request after %s (account: %s)", reason, account.Name)
				continue
			}
			if s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody) {
				upstreamDetail := ""
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
					if maxBytes <= 0 {
						maxBytes = 2048
					}
					upstreamDetail = truncateString(string(respBody), maxBytes)
				}
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					Kind:               "failover",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				s.handleFailoverSideEffects(ctx, resp, account, respBody, upstreamModel)
				return nil, newOpenAIUpstreamFailoverError(
					resp.StatusCode,
					resp.Header,
					respBody,
					upstreamMsg,
					account.IsPoolMode() && (account.IsPoolModeRetryableStatus(resp.StatusCode) || isOpenAITransientProcessingError(resp.StatusCode, upstreamMsg, respBody)),
				)
			}
			return s.handleErrorResponse(ctx, resp, c, account, body, billingModel)
		}
		defer func() { _ = resp.Body.Close() }()
		if mapping, ok := openAIResponsesClientToolMapping(c); ok && reqStream && isEventStreamResponse(resp.Header) {
			maxLineSize := defaultMaxLineSize
			if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
				maxLineSize = s.cfg.Gateway.MaxLineSize
			}
			resp.Body = newResponsesClientToolStreamBody(resp.Body, mapping, maxLineSize, s.cfg)
		}

		serviceTier := extractOpenAIServiceTierFromBody(body)
		if extractOpenAICodexTurnState(resp.Header) != "" {
			s.noteOpenAICodexTurnStateProvenance(c, account)
		}
		// 上游接受后只保留计费需要的标量，避免响应处理期间继续保活完整 input/tools map。
		reqBody = nil

		// Handle normal response
		var usage *OpenAIUsage
		var firstTokenMs *int
		var imageFirstOutputMs *int
		responseID := ""
		imageCount := 0
		var imageOutputSizes []string
		if reqStream {
			streamResult, err := s.handleStreamingResponseWithReasoning(ctx, resp, c, account, startTime, originalModel, upstreamModel, reasoningEffortValue)
			if err != nil {
				if signal, ok := asOpenAICompactFallbackSignal(err); ok {
					if retryBody, fallbackModel, retry := s.prepareOpenAICompactFallbackRetry(c, account, originalModel, body, http.StatusBadRequest, signal.message, signal.payload, compactFallbackRetried); retry {
						compactFallbackRetried = true
						s.appendOpenAICompactFallbackRetryOps(c, account, resp, signal.payload, signal.message, false)
						body = retryBody
						upstreamModel = fallbackModel
						requestView = newOpenAIRequestView(body)
						reqBody = nil
						continue
					}
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account), UpstreamStatusCode: http.StatusBadRequest, Kind: "http_error", Message: signal.message})
					c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": signal.message}})
					return nil, fmt.Errorf("non-streaming openai protocol error: %s", signal.message)
				}
				return nil, err
			}
			usage = streamResult.usage
			firstTokenMs = streamResult.firstTokenMs
			imageFirstOutputMs = streamResult.imageFirstOutputMs
			responseID = strings.TrimSpace(streamResult.responseID)
			imageCount = streamResult.imageCount
			imageOutputSizes = streamResult.imageOutputSizes
		} else {
			nonStreamResult, err := s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, upstreamModel)
			if err != nil {
				if signal, ok := asOpenAICompactFallbackSignal(err); ok {
					if retryBody, fallbackModel, retry := s.prepareOpenAICompactFallbackRetry(c, account, originalModel, body, http.StatusBadRequest, signal.message, signal.payload, compactFallbackRetried); retry {
						compactFallbackRetried = true
						s.appendOpenAICompactFallbackRetryOps(c, account, resp, signal.payload, signal.message, false)
						body = retryBody
						upstreamModel = fallbackModel
						requestView = newOpenAIRequestView(body)
						reqBody = nil
						continue
					}
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account), UpstreamStatusCode: http.StatusBadRequest, Kind: "http_error", Message: signal.message})
					c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": signal.message}})
					return nil, fmt.Errorf("non-streaming openai protocol error: %s", signal.message)
				}
				return nil, err
			}
			usage = nonStreamResult.usage
			responseID = strings.TrimSpace(nonStreamResult.responseID)
			imageCount = nonStreamResult.imageCount
			imageOutputSizes = nonStreamResult.imageOutputSizes
			if imageCount > 0 {
				ms := int(time.Since(startTime).Milliseconds())
				imageFirstOutputMs = &ms
			}
		}
		s.bindHTTPResponseAccount(ctx, c, account, responseID)

		// Extract and save Codex usage snapshot from response headers (for OAuth accounts).
		// 排除 spark 影子:其 codex_* 仅由 QueryUsage(/wham/usage bengalfox)更新(外审第7轮 P1)。
		if account.Type == AccountTypeOAuth && !account.IsShadow() {
			if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
				s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
			}
		}

		if usage == nil {
			usage = &OpenAIUsage{}
		}

		forwardResult := &OpenAIForwardResult{
			RequestID:                     resp.Header.Get("x-request-id"),
			ResponseID:                    responseID,
			Usage:                         *usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			ReasoningEffort:               reasoningEffort,
			Stream:                        reqStream,
			OpenAIWSMode:                  false,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  firstTokenMs,
			ImageFirstOutputMs:            imageFirstOutputMs,
		}
		if imageCount > 0 {
			forwardResult.ImageCount = imageCount
			forwardResult.ImageSize = imageSizeTier
			forwardResult.ImageInputSize = imageInputSize
			forwardResult.ImageOutputSizes = imageOutputSizes
			forwardResult.BillingModel = imageBillingModel
		}
		return forwardResult, nil
	}
}

func shouldForwardOpenAIResponsesViaRawChatCompletions(account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	if account.routesByModel() {
		// Model protocol_rules are the authority. Probe Extra must not collapse
		// Grok/GPT/Muse into Chat Completions.
		return false
	}
	if account.RoutesProtocolByInbound() {
		switch account.GetAPIProtocol() {
		case APIProtocolChatCompletions:
			return true
		case APIProtocolAdaptive:
			return !account.SupportsNativeCNResponses()
		default:
			return false
		}
	}
	return !openai_compat.ShouldUseResponsesAPI(account.Extra)
}

func (s *OpenAIGatewayService) buildUpstreamRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string, isStream bool, promptCacheKey string, isCodexCLI bool) (*http.Request, error) {
	// Determine target URL based on account type
	var targetURL string
	switch account.Type {
	case AccountTypeOAuth, AccountTypeSetupToken:
		// OAuth accounts use ChatGPT internal API
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		// API Key accounts use Platform API or custom base URL
		baseURL := account.GetOpenAIBaseURL()
		if account.UsesNativeCNResponses() && account.IsAdaptiveAPIProtocol() {
			baseURL = account.GetCNProtocolBaseURL(APIProtocolResponses)
		}
		if baseURL == "" {
			targetURL = openaiPlatformAPIURL
		} else {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesURLForPlatform(account.Platform, validatedURL)
		}
	default:
		targetURL = openaiPlatformAPIURL
	}
	targetURL = appendOpenAIResponsesRequestPathSuffix(targetURL, openAIResponsesRequestPathSuffix(c))
	body = normalizeDeepSeekResponsesRequestBody(account, body)
	body = clampOllamaCloudResponsesMaxOutputTokens(account, body)

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Set headers specific to OAuth accounts (ChatGPT internal API)
	if account.UsesOpenAICodexProtocol() {
		// Required: set Host for ChatGPT API (must use req.Host, not Header.Set)
		req.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
			return nil, fmt.Errorf("resolve chatgpt account headers: %w", err)
		}
	}

	// Whitelist passthrough headers
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiAllowedHeaders[lowerKey] {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	if account.UsesOpenAICodexProtocol() {
		applyCodexAccountIdentityHeaders(req.Header, codexAccountIdentitySource(c, account), getAPIKeyIDFromContext(c))
	}
	// 客户端回带的 x-codex-turn-state 若已知由其他账号铸造（failover 换号），
	// 剥离后再出站——异账号 blob 与本账号的（指纹收敛后）出站身份自相矛盾。
	s.guardOpenAICodexTurnStateEcho(c, account, req.Header)
	if account.UsesOpenAICodexProtocol() {
		compatMessagesBridge := isOpenAICompatMessagesBridgeContext(c) || isOpenAICompatMessagesBridgeBody(body)
		// 清除客户端透传的 session 头，后续用隔离后的值重新设置，防止跨用户会话碰撞。
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		req.Header.Del("conversation_id")
		req.Header.Del("session_id")

		if compatMessagesBridge {
			req.Header.Del("OpenAI-Beta")
			req.Header.Del("originator")
		} else {
			req.Header.Set("originator", resolveOpenAIUpstreamOriginator(c, isCodexCLI))
		}
		apiKeyID := getAPIKeyIDFromContext(c)
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", CodexCanonicalClientVersion())
			}
			compactSession := resolveOpenAICompactSessionID(c)
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, compactSession))
		} else {
			req.Header.Set("accept", "text/event-stream")
		}
		if promptCacheKey != "" {
			isolated := isolateOpenAIUpstreamSessionID(apiKeyID, codexAccountIdentitySource(c, account), promptCacheKey)
			req.Header.Set("session_id", isolated)
			if !compatMessagesBridge || clientConversationID != "" {
				req.Header.Set("conversation_id", isolated)
			}
		}
	} else if isOpenAIResponsesCompactPath(c) {
		req.Header.Set("accept", "application/json")
	}

	// Apply custom User-Agent if configured
	customUA := account.GetOpenAIUserAgent()
	if customUA != "" {
		req.Header.Set("user-agent", customUA)
	}

	// 若开启 ForceCodexCLI，则强制将上游 User-Agent 伪装为 Codex CLI。
	// 用于网关未透传/改写 User-Agent 时，仍能命中 Codex 侧识别逻辑。
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		req.Header.Set("user-agent", CodexCanonicalUserAgent())
	}

	// 指纹收敛：请求体与出站头使用同一份预计算 ID。
	applyStagedCodexFingerprintHeaders(c, account, req.Header)

	if account.UsesOpenAICodexProtocol() {
		enforceCodexIdentityHeadersWithUA(req.Header, s.codexIdentityOverrideUA(account))
	}

	// Ensure required headers exist
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}

	// 账号级请求头覆写（仅 openai api_key 账号启用时生效；OAuth 路径 no-op）
	applyOpenCodeUpstreamUserAgent(account, targetURL, req.Header)
	account.ApplyHeaderOverrides(req.Context(), req.Header)
	applyOpenCodeSessionHeader(c, account, targetURL, req.Header, body, openCodeSessionHintBody(promptCacheKey))
	applyOpenAICodexBetaFeatures(c, account, req.Header)
	if account.UsesOpenAICodexProtocol() {
		stripOpenAILegacyResponsesBeta(req.Header)
	}
	setOpenAICodexRoutingHintFromBody(req.Header, account, body)
	logOpenAIRoutingDiagnosticsFromBody(ctx, account, "http", req.Header, body, "not_applicable")
	if err := applyMappedGPT55LiteCompatibility(req, account, body); err != nil {
		return nil, err
	}

	return req, nil
}

// codexIdentityOverrideUA 返回账号级显式配置的出站 User-Agent。
// ForceCodexCLI 开启时返回空串，交给规范身份收口使用默认 CLI UA。
func (s *OpenAIGatewayService) codexIdentityOverrideUA(account *Account) string {
	if s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		return ""
	}
	if account == nil {
		return ""
	}
	return account.GetOpenAIUserAgent()
}
