package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// openaiStreamingResult streaming response result
type openaiStreamingResult struct {
	usage              *OpenAIUsage
	firstTokenMs       *int
	imageFirstOutputMs *int
	responseID         string
	imageCount         int
	imageOutputSizes   []string
	searchCount        int
}

type openaiNonStreamingResult struct {
	*OpenAIUsage
	usage            *OpenAIUsage
	responseID       string
	imageCount       int
	imageOutputSizes []string
	searchCount      int
}

// openaiStreamingResult streaming response result
func (s *OpenAIGatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel string) (*openaiStreamingResult, error) {
	return s.handleStreamingResponseWithReasoning(ctx, resp, c, account, startTime, originalModel, mappedModel, "")
}

func (s *OpenAIGatewayService) handleStreamingResponseWithReasoning(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel, reasoningEffort string) (*openaiStreamingResult, error) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	redactionCtx := ctx
	if c != nil && c.Request != nil {
		redactionCtx = c.Request.Context()
	}
	redactSensitiveBody, redactorErr := s.agentIdentitySensitiveBodyRedactor(redactionCtx, account)
	if redactorErr != nil {
		return nil, fmt.Errorf("resolve agent identity credentials for stream redaction: %w", redactorErr)
	}
	firstOutputTimeout := time.Duration(0)
	if account != nil && account.Platform == PlatformOpenAI {
		firstOutputTimeout = s.openAIFirstOutputTimeout(reasoningEffort)
	}
	guardFirstOutput := firstOutputTimeout > 0
	stageFirstOutput := account != nil && account.Platform == PlatformOpenAI
	var attemptResponseHeaders http.Header
	if stageFirstOutput {
		if s.responseHeaderFilter != nil {
			attemptResponseHeaders = responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter)
		} else if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
			attemptResponseHeaders = http.Header{"X-Request-Id": []string{requestID}}
		}
	} else if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if stageFirstOutput {
		stageOpenAICodexTurnState(&attemptResponseHeaders, resp.Header)
	} else {
		s.relayOpenAICodexTurnState(c, account, resp.Header)
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if !stageFirstOutput && resp.Header.Get("x-request-id") != "" {
		v := resp.Header.Get("x-request-id")
		c.Header("x-request-id", v)
	}
	applyAttemptResponseHeaders := func() {
		if !stageFirstOutput || len(attemptResponseHeaders) == 0 || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		s.noteStagedOpenAICodexTurnStateCommitted(c, account, attemptResponseHeaders)
		// These headers describe this gateway's SSE stream and are stable across
		// account attempts. Keep them authoritative over upstream values.
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	var firstTokenMs *int
	var imageFirstOutputMs *int
	firstOutputProgressObserved := false
	bufferedWriter := bufio.NewWriterSize(w, 4*1024)
	var firstOutputStage *openAIFirstOutputStage
	if stageFirstOutput {
		firstOutputStage = newDefaultOpenAIFirstOutputStage()
		defer func() {
			if err := firstOutputStage.Close(); err != nil {
				logger.LegacyPrintf("service.openai_gateway", "OpenAI first-output staging cleanup failed: account=%d model=%s error=%v", account.ID, originalModel, err)
			}
		}()
	}
	writePendingString := func(value string) (int, error) {
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.WriteString(value)
		}
		return bufferedWriter.WriteString(value)
	}
	pendingBytes := func() int64 {
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.Buffered()
		}
		return int64(bufferedWriter.Buffered())
	}
	flushBuffered := func() error {
		if firstOutputStage != nil && !firstOutputStage.closed {
			if err := firstOutputStage.CommitTo(w); err != nil {
				return err
			}
		} else {
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		flusher.Flush()
		return nil
	}

	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	responseID := ""
	var firstOutputScanGuard atomic.Bool
	firstOutputScanGuard.Store(stageFirstOutput)
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	scanner.Split(newBoundedSSESplit(s.cfg, maxLineSize, nil))
	if stageFirstOutput {
		scanner.Split(newBoundedSSESplit(s.cfg, maxLineSize, openAIFirstOutputDynamicScanLines(&firstOutputScanGuard)))
	}
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	// 仅监控上游数据间隔超时，不被下游写入阻塞影响
	var intervalTimer *time.Timer
	if streamInterval > 0 {
		intervalTimer = time.NewTimer(streamInterval)
		defer intervalTimer.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTimer != nil {
		intervalCh = intervalTimer.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// 下游 keepalive 仅用于防止代理空闲断开
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}

	var firstOutputTimer *time.Timer
	var firstOutputCh <-chan time.Time
	if firstOutputTimeout > 0 {
		remaining := time.Until(startTime.Add(firstOutputTimeout))
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		firstOutputTimer = time.NewTimer(remaining)
		firstOutputCh = firstOutputTimer.C
		defer firstOutputTimer.Stop()
	}
	stopFirstOutputTimer := func() {
		if firstOutputTimer == nil {
			return
		}
		if !firstOutputTimer.Stop() {
			select {
			case <-firstOutputTimer.C:
			default:
			}
		}
		firstOutputTimer = nil
		firstOutputCh = nil
	}
	// Track downstream writes separately from upstream reads: pre-output failover
	// can buffer response.created / response.in_progress, so keepalive must be
	// based on downstream idle time.
	lastDownstreamWriteAt := time.Now()

	// 仅发送一次错误事件，避免多次写入导致协议混乱。
	// 注意：OpenAI `/v1/responses` streaming 事件必须符合 OpenAI Responses schema；
	// 否则下游 SDK（例如 OpenCode）会因为类型校验失败而报错。
	errorEventSent := false
	clientDisconnected := false // 客户端断开后继续 drain 上游以收集 usage
	sawTerminalEvent := false
	sawFailedEvent := false
	responsesSemanticOutputSeen := false
	capacityFailoverSuppressedLogged := false
	failedMessage := ""
	var createdAtStamper responsesCreatedAtStamper
	bareErrorPending := false
	bareErrorPayload := []byte(nil)
	bareErrorJustResolved := false
	suppressNextSSEBoundary := false
	clientOutputStarted := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	var streamEarlyErr error
	eventInProgress := false
	eventStartsClientOutput := false
	eventStartsVisibleOutput := false
	eventShouldFlush := false
	handlePendingWriteError := func(err error) {
		if firstOutputStage != nil && !firstOutputStage.closed {
			message := "OpenAI first-output staging failed"
			if errors.Is(err, errOpenAIFirstOutputStageLimit) {
				message = "OpenAI first-output staging limit exceeded"
			}
			logger.LegacyPrintf("service.openai_gateway", "%s: account=%d model=%s error=%v", message, account.ID, originalModel, err)
			failoverErr := s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, message)
			failoverErr.SafeToFailoverAfterWrite = true
			streamEarlyErr = failoverErr
			_ = resp.Body.Close()
			return
		}
		clientDisconnected = true
		logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
	}
	completeGuardedEvent := func(queueDrained bool) {
		completedProgressEvent := eventStartsClientOutput
		completedVisibleEvent := eventStartsVisibleOutput
		shouldFlush := eventShouldFlush || (queueDrained && clientOutputStarted)
		eventInProgress = false
		if !clientDisconnected {
			if completedProgressEvent {
				applyAttemptResponseHeaders()
			}
			if shouldFlush {
				if err := flushBuffered(); err != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
				} else {
					clientOutputStarted = true
					lastDownstreamWriteAt = time.Now()
				}
			}
		}
		if completedProgressEvent && !firstOutputProgressObserved {
			firstOutputScanGuard.Store(false)
			firstOutputProgressObserved = true
			stopFirstOutputTimer()
		}
		if completedVisibleEvent && firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		eventStartsClientOutput = false
		eventStartsVisibleOutput = false
		eventShouldFlush = false
	}
	sendResponsesStreamError := func(code string) {
		if errorEventSent || clientDisconnected {
			return
		}
		errorEventSent = true
		payload := `{"code":` + strconv.Quote(code) + `,"message":` + strconv.Quote(code) + `,"param":null}`
		if c != nil && c.Request != nil && strings.HasSuffix(c.Request.URL.Path, "/responses") {
			payload = `{"type":"error","code":` + strconv.Quote(code) + `,"message":` + strconv.Quote(code) + `,"param":null}`
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		if _, err := writePendingString("event: error\ndata: " + payload + "\n\n"); err != nil {
			clientDisconnected = true
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
		MarkResponseCommitted(c)
		lastDownstreamWriteAt = time.Now()
	}
	synthesizeBareError := func() {
		if !bareErrorPending {
			return
		}
		bareErrorPending = false
		msg := extractOpenAISSEErrorMessage(bareErrorPayload)
		if msg == "" {
			msg = "Upstream request failed"
		}
		failedMessage = msg
		failedSSE := buildOpenAIResponseFailedSSE(responseID, originalModel, createdAtStamper.Value(), bareErrorPayload, msg)
		sawTerminalEvent = true
		sawFailedEvent = true
		s.parseSSEUsageBytes(bareErrorPayload, usage)
		markOpenAICyberPolicyEvent(c, bareErrorPayload, http.StatusOK, usage)
		status, _ := s.handleOpenAIStreamTerminalAccountSideEffects(c, account, bareErrorPayload, msg, resp.Header, mappedModel)
		s.recordOpenAIStreamUpstreamErrorWithStatus(c, account, false, upstreamRequestID, "stream_failed", bareErrorPayload, msg, status)
		if clientDisconnected {
			return
		}
		if _, err := writePendingString(failedSSE); err != nil {
			clientDisconnected = true
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
	}

	needModelReplace := originalModel != mappedModel
	streamOutputAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamOutputAccumulator.XAIServerSideSearch = account != nil && account.IsGrok()
	streamDoneItems := newResponsesStreamOutputItems()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	searchCount := 0
	streamSearchSeen := make(map[string]struct{})
	resultWithUsage := func() *openaiStreamingResult {
		return &openaiStreamingResult{
			usage:              usage,
			firstTokenMs:       firstTokenMs,
			imageFirstOutputMs: imageFirstOutputMs,
			responseID:         responseID,
			imageCount:         imageCounter.Count(),
			imageOutputSizes:   imageCounter.Sizes(),
			searchCount:        searchCount,
		}
	}
	flushPending := func(disconnectMessage string) {
		if clientDisconnected || pendingBytes() == 0 {
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			logger.LegacyPrintf("service.openai_gateway", "%s", disconnectMessage)
			return
		}
		clientOutputStarted = true
		MarkResponseCommitted(c)
		lastDownstreamWriteAt = time.Now()
	}
	finalizeStream := func() (*openaiStreamingResult, error) {
		if bareErrorPending {
			synthesizeBareError()
		}
		if stageFirstOutput && eventInProgress {
			// EOF dispatches the final SSE event even without a trailing blank line.
			completeGuardedEvent(true)
		}
		if sawTerminalEvent && !sawFailedEvent {
			s.clearOpenAIProxyStreamDisconnect(account)
		}
		if !sawTerminalEvent && !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				false,
				upstreamRequestID,
				nil,
				"OpenAI stream ended before a terminal event",
			)
		}
		flushPending("Client disconnected during final flush, returning collected usage")
		if !sawTerminalEvent {
			if openAIStreamClientOutputStarted(c, clientOutputStarted) && !clientDisconnected {
				s.recordOpenAIProxyStreamDisconnect(account, errors.New("stream ended before terminal event"), upstreamRequestID)
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		if sawFailedEvent {
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
		}
		return resultWithUsage(), nil
	}
	handleScanErr := func(scanErr error) (*openaiStreamingResult, error, bool) {
		if scanErr == nil {
			return nil, nil, false
		}
		safeScanErr := redactAgentIdentitySensitiveError(redactSensitiveBody, scanErr)
		if errors.Is(scanErr, errOpenAIFirstOutputScannerLimit) && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE token exceeded guarded first-output limit: account=%d limit=%d error=%v", account.ID, openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance, safeScanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if errors.Is(scanErr, errSSETransportLimit) && stageFirstOutput && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE event exceeded transport limit before first output: account=%d error=%v", account.ID, scanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE event exceeds first-output transport limit: "+scanErr.Error(),
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) && stageFirstOutput && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long before first output: account=%d max_size=%d error=%v", account.ID, maxLineSize, safeScanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if sawTerminalEvent {
			if !sawFailedEvent {
				s.clearOpenAIProxyStreamDisconnect(account)
				logger.LegacyPrintf("service.openai_gateway", "Upstream scan ended after terminal event: %v", safeScanErr)
			}
			result, err := finalizeStream()
			return result, err, true
		}
		if bareErrorPending {
			synthesizeBareError()
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage), true
		}
		// 客户端断开/取消请求时，上游读取往往会返回 context canceled。
		// /v1/responses 的 SSE 事件必须符合 OpenAI 协议；这里不注入自定义 error event，避免下游 SDK 解析失败。
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			if eventShouldFlush {
				flushPending("Client disconnected during canceled stream flush, returning collected usage")
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", safeScanErr), true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, safeScanErr)
			sendResponsesStreamError("response_too_large")
			return resultWithUsage(), safeScanErr, true
		}
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(safeScanErr.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(), s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, msg), true
		}
		// 客户端已断开时，上游出错仅影响体验，不影响计费；返回已收集 usage
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", safeScanErr), true
		}
		s.recordOpenAIProxyStreamDisconnect(account, scanErr, upstreamRequestID)
		code := "upstream_stream_read_error"
		if strings.Contains(strings.ToLower(safeScanErr.Error()), "stream id") {
			code = "upstream_http2_stream_error"
		}
		sendResponsesStreamError(code)
		return resultWithUsage(), fmt.Errorf("stream read error: %w", safeScanErr), true
	}
	// Upstream keepalive events bypass the first-output stage the same way the
	// comment keepalive does: Codex resets its stream idle timer only on parsed
	// SSE events, while the attempt's lifecycle events and account headers stay
	// private until semantic output. The bytes do not count as client output.
	keepaliveEventDirect := false
	forwardsKeepaliveDirect := func(eventType string) bool {
		return eventType == "keepalive" && stageFirstOutput && !firstOutputStage.closed &&
			!sawFailedEvent && !bareErrorPending && streamEarlyErr == nil
	}
	writeKeepaliveDirect := func(value string) {
		if clientDisconnected {
			return
		}
		n, err := w.Write([]byte(value))
		recordOpenAIStreamKeepaliveBytes(c, n)
		if err != nil {
			clientDisconnected = true
			logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
		}
	}
	processSSELine := func(line string, queueDrained bool) {
		if streamEarlyErr != nil {
			return
		}
		if eventType, ok := extractOpenAISSEEventLine(line); ok && forwardsKeepaliveDirect(strings.TrimSpace(eventType)) {
			keepaliveEventDirect = true
			eventInProgress = true
			writeKeepaliveDirect(line + "\n")
			return
		}
		if line == "event: error" && account != nil && account.Type != AccountTypeAPIKey {
			return
		}
		if line == "" && suppressNextSSEBoundary {
			suppressNextSSEBoundary = false
			return
		}
		// Extract data from SSE line (supports both "data: " and "data:" formats)
		if data, ok := extractOpenAISSEDataLine(line); ok {
			bareErrorJustResolved = false
			if strings.TrimSpace(data) == "[DONE]" && bareErrorPending {
				suppressNextSSEBoundary = true
				synthesizeBareError()
				streamEarlyErr = fmt.Errorf("upstream response failed: %s", failedMessage)
				return
			}
			if strings.TrimSpace(data) == "[DONE]" && sawFailedEvent && account != nil && account.Type != AccountTypeAPIKey {
				suppressNextSSEBoundary = true
				return
			}
			dataBytes := []byte(data)
			eventTypeRaw := gjson.GetBytes(dataBytes, "type").String()
			eventType := strings.TrimSpace(eventTypeRaw)
			if keepaliveEventDirect || forwardsKeepaliveDirect(eventType) {
				keepaliveEventDirect = true
				eventInProgress = true
				writeKeepaliveDirect(line + "\n")
				return
			}
			if stamped, changed := createdAtStamper.Stamp(dataBytes, eventType); changed {
				dataBytes = stamped
				data = string(stamped)
				line = "data: " + data
			}
			observer.ObserveOpenAI(dataBytes, eventTypeRaw)
			if isOpenAIErrorBearingEventType(eventType) {
				if redacted := redactSensitiveBody(dataBytes); !bytes.Equal(redacted, dataBytes) {
					dataBytes = redacted
					data = string(redacted)
					line = "data: " + data
				}
			}
			// 初始上游 data 的 type 只解析一次：原始值用于终止事件精确匹配，规范化值供后续分支复用。
			if (eventType != "error" || account == nil || account.Type == AccountTypeAPIKey) && openAIStreamEventIsTerminalWithType(data, eventTypeRaw) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			forceFlushFailedEvent := false
			if !capacityFailoverSuppressedLogged && account != nil && account.Platform == PlatformOpenAI &&
				(eventType == "error" || eventType == "response.failed") &&
				openAIStreamClientOutputStarted(c, clientOutputStarted) &&
				isOpenAIUpstreamCapacityShedEvent(dataBytes) {
				logOpenAICapacityFailoverSuppressed(ctx, account, "native_sse", upstreamRequestID, eventType)
				capacityFailoverSuppressedLogged = true
			}
			if eventType == "error" && !openAIStreamClientOutputStarted(c, clientOutputStarted) {
				errorMessage := extractOpenAISSEErrorMessage(dataBytes)
				s.parseSSEUsageBytes(dataBytes, usage)
				if hit, _, msg := detectOpenAICyberPolicy(dataBytes); hit {
					markOpenAICyberPolicyEvent(c, dataBytes, http.StatusOK, usage)
					c.Data(http.StatusBadRequest, "application/json", dataBytes)
					streamEarlyErr = fmt.Errorf("openai cyber_policy: %s", msg)
					return
				}
				if isOpenAIUpstreamAccessStateError(errorMessage, dataBytes) {
					streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, errorMessage, mappedModel, resp.Header)
					return
				}
				if isOpenAIUpstreamCapacityShedEvent(dataBytes) {
					streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, errorMessage, mappedModel, resp.Header)
					return
				}
				if account != nil && account.Type != AccountTypeAPIKey {
					bareErrorPending = true
					bareErrorPayload = append(bareErrorPayload[:0], dataBytes...)
					suppressNextSSEBoundary = true
					return
				}
				if account != nil && account.Type == AccountTypeAPIKey {
					sawFailedEvent = true
					failedMessage = errorMessage
				}
				if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, account.Platform, dataBytes, errorMessage); matched {
					s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "http_error", dataBytes, errorMessage)
					MarkResponseCommitted(c)
					c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
					c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": errMsg}})
					streamEarlyErr = fmt.Errorf("upstream error event: passthrough rule matched message=%s", errMsg)
					return
				}
				if openAIStreamErrorEventShouldFailover(dataBytes, errorMessage) {
					streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, errorMessage, mappedModel, resp.Header)
					return
				}
			}
			if eventType == "error" && account != nil && account.Platform == PlatformOpenAI && account.Type != AccountTypeAPIKey {
				bareErrorPending = true
				bareErrorPayload = append(bareErrorPayload[:0], dataBytes...)
				suppressNextSSEBoundary = true
				return
			}
			if eventType == "response.failed" {
				outputAlreadyStarted := openAIStreamClientOutputStarted(c, clientOutputStarted)
				bareErrorJustResolved = bareErrorPending
				bareErrorPending = false
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				// Paired failures can arrive after visible output, so account
				// health side effects must run even when failover is suppressed.
				statusCode, _ := s.handleOpenAIStreamTerminalAccountSideEffects(c, account, dataBytes, failedMessage, resp.Header)
				// response.failed 自带上游已消耗的 usage（input token 通常已扣）；必须先解析
				// 再打 cyber 标记，否则 mark 记到的是解析前的 0，导致流式 cyber 按 0 token 计费
				// 而漏记真实用量。对齐 WS V2 / Chat 流式路径（均先解析 usage 再 Mark）。
				s.parseSSEUsageBytes(dataBytes, usage)
				if outputAlreadyStarted {
					s.recordOpenAIStreamUpstreamErrorWithStatus(c, account, false, upstreamRequestID, "stream_failed", dataBytes, failedMessage, statusCode)
				}
				if hit, code, msg := detectOpenAICyberPolicy(dataBytes); hit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
					if isExplicitOpenAICompactRequest(c, nil) && isOpenAICompactModelFailure(http.StatusBadRequest, failedMessage, dataBytes) {
						sawFailedEvent = true
						streamEarlyErr = newOpenAICompactFallbackSignal(c, dataBytes, failedMessage)
						if streamEarlyErr == nil {
							streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header)
						}
						return
					}
					if isOpenAIUpstreamAccessStateError(failedMessage, dataBytes) {
						sawFailedEvent = true
						streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header)
						return
					}
					if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, account.Platform, dataBytes, failedMessage); matched {
						sawFailedEvent = true
						// 命中透传规则也要记录 ops 上游错误事件（对齐 CC/Messages 与
						// antigravity 先例），否则透传命中的 failed 在监控中不可见。
						s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "http_error", dataBytes, failedMessage)
						MarkResponseCommitted(c)
						c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
						c.JSON(status, gin.H{
							"error": gin.H{
								"type":    errType,
								"message": errMsg,
							},
						})
						streamEarlyErr = fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
						return
					}
					if openAIStreamFailedEventShouldFailover(dataBytes, failedMessage) {
						sawFailedEvent = true
						streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header)
						return
					}
				}
				forceFlushFailedEvent = !bareErrorPending && !bareErrorJustResolved
				sawFailedEvent = true
			}
			if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
			}
			imageCounter.AddSSEData(dataBytes)
			searchCount += countGrokNativeSearchCallsInSSEDataDedup(dataBytes, streamSearchSeen)
			searchCount = grokSearchCountWithTerminalUsage(searchCount, dataBytes)
			if imageFirstOutputMs == nil && openAISSEDataContainsImageOutput(dataBytes) {
				ms := int(time.Since(startTime).Milliseconds())
				imageFirstOutputMs = &ms
			}

			// Correct Codex tool calls if needed (apply_patch -> edit, etc.)
			if correctedData, corrected := s.toolCorrector.CorrectToolCallsInSSEBytes(dataBytes); corrected {
				dataBytes = correctedData
				data = string(correctedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			if imageOutput, ok := extractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			streamDoneItems.Observe(dataBytes)
			if responsesStreamEventMayContributeToOutput(eventType) {
				var streamEvent apicompat.ResponsesStreamEvent
				if err := json.Unmarshal(dataBytes, &streamEvent); err == nil {
					streamOutputAccumulator.ProcessEvent(&streamEvent)
				}
			}
			if eventType == "response.completed" || eventType == "response.done" {
				bareErrorPending = false
			}
			// Completed output_item.done items are authoritative when present;
			// otherwise rebuild from the delta accumulator (fork fallback).
			terminalOutputArgs := []any{streamOutputAccumulator, streamImageOutputs}
			if streamDoneItems.HasItems() {
				terminalOutputArgs = []any{streamOutputAccumulator, streamDoneItems, streamImageOutputs}
			}
			if normalizedData, normalized := normalizeResponsesStreamingTerminalOutput(dataBytes, terminalOutputArgs...); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
				return
			}
			restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
				return
			}
			if mapping, ok := openAIResponsesClientToolMapping(c); ok {
				restoredData, _, restoreErr = apicompat.RestoreResponsesClientToolPayload(restoredData, mapping)
				if restoreErr != nil {
					streamEarlyErr = fmt.Errorf("restore OpenAI Responses client tools: %w", restoreErr)
					return
				}
			}
			restoredData = restoreCodexToolNamesFromSSEContext(c, restoredData, eventType)
			restoredData = restoreOpenAIResponsesInstructionsEchoInEvent(c, restoredData)
			if !bytes.Equal(restoredData, dataBytes) {
				dataBytes = restoredData
				data = string(restoredData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			if sanitizedData, sanitized := sanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				openAIStreamClientOutputStarted(c, clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				data = string(sanitizedData)
				line = "data: " + data
			}
			if needModelReplace {
				line = s.replaceModelInSSELine(line, mappedModel, originalModel)
			}
			startsClientOutput := forceFlushFailedEvent || openAIStreamDataStartsClientOutput(data, eventType)
			startsVisibleOutput := openAIStreamDataStartsVisibleOutput(data, eventType)
			if stageFirstOutput {
				eventStartsClientOutput = eventStartsClientOutput || startsClientOutput
				eventStartsVisibleOutput = eventStartsVisibleOutput || startsVisibleOutput
				if startsClientOutput {
					firstOutputScanGuard.Store(false)
				}
			}
			if startsClientOutput && !openAIStreamEventTypeIsTerminal(eventType) {
				responsesSemanticOutputSeen = true
			}
			if account != nil && (account.Platform == PlatformOpenAI || account.Platform == PlatformGrok) &&
				(eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !responsesSemanticOutputSeen && !clientOutputStarted &&
				openAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				sawTerminalEvent = true
				streamEarlyErr = newOpenAIResponsesEmptyCompletedFailoverError(c, account, upstreamRequestID)
				return
			}

			// 写入客户端（客户端断开后继续 drain 上游）
			suppressCapacityErrorEvent := eventType == "error" &&
				openAIStreamClientOutputStarted(c, clientOutputStarted) &&
				isOpenAIUpstreamCapacityShedEvent(dataBytes)
			if !clientDisconnected && !suppressCapacityErrorEvent {
				shouldFlush := queueDrained && (clientOutputStarted || startsClientOutput)
				if bareErrorJustResolved {
					shouldFlush = false
					eventShouldFlush = true
				}
				if firstTokenMs == nil && startsVisibleOutput {
					// 保证首个 token 事件尽快出站，避免影响 TTFT。
					shouldFlush = true
				}
				eventShouldFlush = eventShouldFlush || shouldFlush
				if _, err := writePendingString(line); err != nil {
					handlePendingWriteError(err)
				} else if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				} else {
					eventInProgress = true
				}
			}

			// Record first token time
			if !guardFirstOutput && firstTokenMs == nil && startsVisibleOutput {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
				stopFirstOutputTimer()
			}
			s.parseSSEUsageBytes(dataBytes, usage)
			return
		}

		// A blank line dispatches a guarded event from the attempt-local stage.
		if stageFirstOutput && line == "" {
			if keepaliveEventDirect {
				keepaliveEventDirect = false
				eventInProgress = false
				writeKeepaliveDirect("\n")
				if !clientDisconnected {
					flusher.Flush()
					lastDownstreamWriteAt = time.Now()
				}
				return
			}
			if !clientDisconnected {
				if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				}
			}
			if streamEarlyErr == nil {
				completeGuardedEvent(queueDrained)
			}
			return
		}
		// Non-guarded streams retain upstream's event-boundary flushing: a keepalive
		// or queue-drain flush must never split an open SSE event.
		shouldFlush := false
		if line == "" {
			shouldFlush = eventShouldFlush || (queueDrained && clientOutputStarted)
			eventShouldFlush = false
		}
		if !clientDisconnected {
			if _, err := writePendingString(line); err != nil {
				handlePendingWriteError(err)
			} else if _, err := writePendingString("\n"); err != nil {
				handlePendingWriteError(err)
			} else {
				eventInProgress = line != ""
				if shouldFlush {
					if err := flushBuffered(); err != nil {
						clientDisconnected = true
						logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
					} else {
						clientOutputStarted = true
						lastDownstreamWriteAt = time.Now()
					}
				}
			}
		}
	}

	// 无超时/无 keepalive 的常见路径走同步扫描，减少 goroutine 与 channel 开销。
	if streamInterval <= 0 && keepaliveInterval <= 0 && firstOutputTimeout <= 0 {
		defer putSSEScannerBuf64K(scanBuf)
		for documentScanner.Scan() {
			processSSELine(documentScanner.Text(), true)
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}
		}
		if result, err, done := handleScanErr(documentScanner.Err()); done {
			return result, err
		}
		return finalizeStream()
	}

	type scanEvent struct {
		line      string
		err       error
		processed chan struct{}
	}
	// 独立 goroutine 读取上游，避免读取阻塞影响 keepalive/超时处理
	// Guard mode permits one queued token plus the token being processed. With
	// the guarded scanner cap this bounds scanner/channel retention near 16 MiB;
	// the timeout-disabled path preserves the legacy depth of 16.
	events := make(chan scanEvent, openAIFirstOutputEventQueueSize(guardFirstOutput))
	done := make(chan struct{})
	queueBudget := newSSEQueueBudget(s.cfg)
	sendEvent := func(ev scanEvent) bool {
		if !queueBudget.acquire(len(ev.line), done) {
			return false
		}
		if firstOutputScanGuard.Load() {
			ev.processed = make(chan struct{})
		}
		select {
		case events <- ev:
		case <-done:
			queueBudget.release(len(ev.line))
			return false
		}
		if ev.processed == nil {
			return true
		}
		select {
		case <-ev.processed:
			return true
		case <-done:
			return false
		}
	}
	markEventProcessed := func(ev scanEvent) {
		if ev.processed != nil {
			close(ev.processed)
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for documentScanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: documentScanner.Text()}) {
				return
			}
		}
		if err := documentScanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	for {
		select {
		case ev, ok := <-events:
			if ok {
				queueBudget.release(len(ev.line))
			}
			if !ok {
				if stageFirstOutput && eventInProgress {
					// EOF dispatches the final SSE event even without a trailing blank
					// line. Do not synthesize extra bytes on the downstream wire.
					completeGuardedEvent(true)
				}
				return finalizeStream()
			}
			if result, err, done := handleScanErr(ev.err); done {
				markEventProcessed(ev)
				return result, err
			}
			processSSELine(ev.line, len(events) == 0)
			markEventProcessed(ev)
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}
			if sawTerminalEvent && ev.line == "" {
				return finalizeStream()
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if remaining := streamInterval - time.Since(lastRead); remaining > 0 {
				// Re-arm for the actual last-read deadline.
				intervalTimer.Reset(remaining)
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			if bareErrorPending {
				synthesizeBareError()
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			logger.LegacyPrintf("service.openai_gateway", "Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			if account != nil && account.Platform == PlatformOpenAI &&
				!openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
				_ = resp.Body.Close()
				failoverErr := s.newOpenAIStreamFailoverErrorWithModel(
					c, account, false, upstreamRequestID, nil,
					"OpenAI upstream stream idle timeout before model output", mappedModel, resp.Header,
				)
				// Only keepalive comments can have reached the client. Joining the
				// first-output failover budget also bounds the replay to one account
				// switch, so one stalled request cannot walk the whole pool and apply
				// HandleStreamTimeout to every account it touches.
				failoverErr.SafeToFailoverAfterWrite = true
				return resultWithUsage(), failoverErr
			}
			sendResponsesStreamError("stream_timeout")
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-firstOutputCh:
			if firstOutputProgressObserved {
				stopFirstOutputTimer()
				continue
			}
			if bareErrorPending {
				synthesizeBareError()
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			_ = resp.Body.Close()
			for ev := range events {
				markEventProcessed(ev)
			}
			return resultWithUsage(), s.newOpenAIFirstOutputTimeoutError(
				ctx, c, account, nil, "", startTime, originalModel, reasoningEffort,
				firstOutputTimeout, "semantic_output", resp.Header,
			)

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if eventInProgress {
				continue
			}
			if time.Since(lastDownstreamWriteAt) < keepaliveInterval {
				continue
			}
			if stageFirstOutput {
				// Bypass attempt-local buffered frames. The stable SSE headers may be
				// committed here, but account headers remain private until semantic output.
				n, err := w.Write([]byte(":\n\n"))
				recordOpenAIStreamKeepaliveBytes(c, n)
				if err != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
					continue
				}
				flusher.Flush()
				lastDownstreamWriteAt = time.Now()
				continue
			}
			if _, err := writePendingString(":\n\n"); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
				continue
			}
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during keepalive flush, continuing to drain upstream for billing")
			} else {
				lastDownstreamWriteAt = time.Now()
			}
		}
	}

}

func extractOpenAISSEDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	start := len("data:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return line[start:], true
}

func extractOpenAISSEEventLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "event:") {
		return "", false
	}
	start := len("event:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return strings.TrimSpace(line[start:]), true
}

type openAICompatSSEFrame struct {
	EventType string
	Data      string
}

type openAICompatSSEFrameParser struct {
	eventType string
	dataLines []string
}

func (p *openAICompatSSEFrameParser) AddLine(line string) (openAICompatSSEFrame, bool) {
	if line == "" {
		return p.dispatch()
	}
	if strings.HasPrefix(line, ":") {
		return openAICompatSSEFrame{}, false
	}
	if eventType, ok := extractOpenAISSEEventLine(line); ok {
		p.eventType = eventType
		return openAICompatSSEFrame{}, false
	}
	if data, ok := extractOpenAISSEDataLine(line); ok {
		p.dataLines = append(p.dataLines, data)
	}
	return openAICompatSSEFrame{}, false
}

func (p *openAICompatSSEFrameParser) Finish() (openAICompatSSEFrame, bool) {
	return p.dispatch()
}

func (p *openAICompatSSEFrameParser) dispatch() (openAICompatSSEFrame, bool) {
	frame := openAICompatSSEFrame{
		EventType: p.eventType,
		Data:      strings.Join(p.dataLines, "\n"),
	}
	p.eventType = ""
	p.dataLines = nil
	return frame, frame.Data != ""
}

func openAICompatPayloadWithEventType(payload, eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || strings.TrimSpace(payload) == "" || strings.TrimSpace(payload) == "[DONE]" {
		return payload
	}
	if value := strings.TrimSpace(gjson.Get(payload, "type").String()); value != "" {
		return payload
	}
	patched, err := sjson.Set(payload, "type", eventType)
	if err != nil {
		return payload
	}
	return patched
}

func effectiveOpenAISSEEventType(payload []byte, eventType string) string {
	if payloadType := strings.TrimSpace(gjson.GetBytes(payload, "type").String()); payloadType != "" {
		return payloadType
	}
	return strings.TrimSpace(eventType)
}

func (s *OpenAIGatewayService) replaceModelInSSELine(line, fromModel, toModel string) string {
	data, ok := extractOpenAISSEDataLine(line)
	if !ok {
		return line
	}
	if data == "" || data == "[DONE]" {
		return line
	}

	if fromModel == "" || toModel == "" || fromModel == toModel || !gjson.Valid(data) {
		return line
	}
	changed := false
	for _, path := range []string{"model", "response.model"} {
		if m := gjson.Get(data, path); m.Exists() && m.Type == gjson.String {
			newData, err := sjson.Set(data, path, toModel)
			if err != nil {
				return line
			}
			data = newData
			changed = true
		}
	}
	if changed {
		return "data: " + data
	}
	return line
}

// correctToolCallsInResponseBody 修正响应体中的工具调用
func (s *OpenAIGatewayService) correctToolCallsInResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	corrected, changed := s.getToolCorrector().CorrectToolCallsInSSEBytes(body)
	if changed {
		body = corrected
	}
	if normalized, changed := normalizeOpenAIResponsesFunctionCallArguments(body); changed {
		body = normalized
	}
	return body
}

func normalizeOpenAIResponsesFunctionCallArguments(data []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(data)) == 0 || !bytes.Contains(data, []byte(`"arguments"`)) {
		return data, false
	}
	if !gjson.ValidBytes(data) {
		return data, false
	}

	updated := data
	changed := false
	setDedupedArgument := func(path string) {
		arg := gjson.GetBytes(updated, path)
		if !arg.Exists() || arg.Type != gjson.String {
			return
		}
		deduped, ok := dedupeRepeatedJSONArgumentString(arg.Str)
		if !ok {
			return
		}
		next, err := sjson.SetBytes(updated, path, deduped)
		if err != nil {
			return
		}
		updated = next
		changed = true
	}

	eventType := strings.TrimSpace(gjson.GetBytes(updated, "type").String())
	if eventType == "response.function_call_arguments.done" {
		setDedupedArgument("arguments")
	}
	if itemType := strings.TrimSpace(gjson.GetBytes(updated, "item.type").String()); isResponsesFunctionCallItemType(itemType) {
		setDedupedArgument("item.arguments")
	}
	dedupeResponsesFunctionCallOutputArguments(updated, "response.output", setDedupedArgument)
	dedupeResponsesFunctionCallOutputArguments(updated, "output", setDedupedArgument)

	return updated, changed
}

func dedupeResponsesFunctionCallOutputArguments(data []byte, outputPath string, setDedupedArgument func(string)) {
	output := gjson.GetBytes(data, outputPath)
	if !output.Exists() || !output.IsArray() {
		return
	}
	for i, item := range output.Array() {
		if !isResponsesFunctionCallItemType(strings.TrimSpace(item.Get("type").String())) {
			continue
		}
		setDedupedArgument(outputPath + "." + strconv.Itoa(i) + ".arguments")
	}
}

func isResponsesFunctionCallItemType(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func dedupeRepeatedJSONArgumentString(arguments string) (string, bool) {
	if len(arguments) == 0 || len(arguments)%2 != 0 {
		return "", false
	}
	halfLen := len(arguments) / 2
	first := arguments[:halfLen]
	if first != arguments[halfLen:] {
		return "", false
	}
	trimmed := strings.TrimSpace(first)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return "", false
	}
	if !json.Valid([]byte(first)) {
		return "", false
	}
	return first, true
}

func (s *OpenAIGatewayService) parseSSEUsage(data string, usage *OpenAIUsage) {
	s.parseSSEUsageBytes([]byte(data), usage)
}

func (s *OpenAIGatewayService) parseSSEUsageBytes(data []byte, usage *OpenAIUsage) {
	if usage == nil || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	eventType := gjson.GetBytes(data, "type").String()
	s.parseSSEUsageBytesWithType(data, eventType, usage)
}

func (s *OpenAIGatewayService) parseSSEUsageBytesWithType(data []byte, eventType string, usage *OpenAIUsage) {
	if usage == nil || len(data) == 0 || !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	parsed, ok := extractOpenAIUsageFromJSONBytes(data)
	if !ok {
		return
	}
	if openAIStreamEventTypeIsTerminal(effectiveOpenAISSEEventType(data, eventType)) {
		if !openAIUsageHasTokens(&parsed) && openAIUsageHasTokens(usage) {
			return
		}
		*usage = parsed
		return
	}
	mergeOpenAIUsageNonZero(usage, parsed)
}

func mergeOpenAIUsageNonZero(dst *OpenAIUsage, src OpenAIUsage) {
	if dst == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.ImageInputTokens > 0 {
		dst.ImageInputTokens = src.ImageInputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		dst.CacheCreation30mInputTokens = src.CacheCreation30mInputTokens
		dst.CacheCreation12hInputTokens = src.CacheCreation12hInputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.ImageOutputTokens > 0 {
		dst.ImageOutputTokens = src.ImageOutputTokens
	}
	if src.ImageCacheReadTokens > 0 {
		dst.ImageCacheReadTokens = src.ImageCacheReadTokens
	}
}

func openAIUsageHasTokens(usage *OpenAIUsage) bool {
	return usage != nil && (usage.InputTokens > 0 || usage.ImageInputTokens > 0 || usage.OutputTokens > 0 || usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 || usage.ImageOutputTokens > 0)
}

const openAIMissingUsageLogInterval = time.Minute //nolint:unused

type openAIMissingUsageLogSampler struct { //nolint:unused
	total      atomic.Uint64
	suppressed atomic.Uint64
	lastLog    atomic.Int64
}

func (s *openAIMissingUsageLogSampler) sample(now time.Time) (bool, uint64, uint64) { //nolint:unused
	total := s.total.Add(1)
	nowNanos := now.UnixNano()
	for {
		last := s.lastLog.Load()
		if last != 0 && nowNanos-last < int64(openAIMissingUsageLogInterval) {
			s.suppressed.Add(1)
			return false, total, 0
		}
		if s.lastLog.CompareAndSwap(last, nowNanos) {
			return true, total, s.suppressed.Swap(0)
		}
	}
}

func logOpenAISuccessMissingUsage(_ context.Context, _ *gin.Context, _ *Account, resp *http.Response, usage *OpenAIUsage, terminalEvent string, _ bool) {
	if resp == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || openAIUsageHasTokens(usage) {
		return
	}
	if terminalEvent != "response.completed" && terminalEvent != "response.done" && terminalEvent != "[DONE]" && terminalEvent != "json" {
		return
	}
}

func extractOpenAIUsageFromJSONBytes(body []byte) (OpenAIUsage, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return OpenAIUsage{}, false
	}
	candidates := []struct{ usagePath, imageUsagePath string }{
		{"usage", "tool_usage.image_gen"},
		{"response.usage", "response.tool_usage.image_gen"},
		{"data.usage", "data.tool_usage.image_gen"},
		{"data.response.usage", "data.response.tool_usage.image_gen"},
	}
	for _, candidate := range candidates {
		if usage, ok := openAIUsageFromGJSON(gjson.GetBytes(body, candidate.usagePath)); ok {
			mergeHostedImageGenToolUsage(gjson.GetBytes(body, candidate.imageUsagePath), &usage)
			return usage, true
		}
	}
	return OpenAIUsage{}, false
}

func openAIResponsesCompletedEventIsEmpty(data []byte, usage *OpenAIUsage) bool {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return false
	}
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ImageInputTokens > 0 || usage.ImageOutputTokens > 0 || usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0) {
		return false
	}
	if gjson.GetBytes(data, "usage").Exists() || gjson.GetBytes(data, "response.usage").Exists() || gjson.GetBytes(data, "error").Exists() || gjson.GetBytes(data, "response.error").Exists() {
		return false
	}
	if output := gjson.GetBytes(data, "response.output"); output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	return true
}

func mergeHostedImageGenToolUsage(imageGen gjson.Result, usage *OpenAIUsage) {
	if !imageGen.Exists() || !imageGen.IsObject() {
		return
	}
	if usage.ImageOutputTokens == 0 {
		if value := imageGen.Get("output_tokens_details.image_tokens").Int(); value > 0 {
			usage.ImageOutputTokens = int(value)
		}
	}
	if usage.ImageInputTokens == 0 {
		if value := imageGen.Get("input_tokens_details.image_tokens").Int(); value > 0 {
			usage.ImageInputTokens = int(value)
		}
	}
}

func extractOpenAIResponseIDFromJSONBytes(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "id").String()); id != "" {
		return id
	}
	return strings.TrimSpace(gjson.GetBytes(body, "response.id").String())
}

func SetOpenAIHTTPResponseOwner(c *gin.Context, userID, apiKeyID int64) {
	if c != nil && userID > 0 && apiKeyID > 0 {
		c.Set(openAIHTTPResponseOwnerContextKeyCompat, struct{ userID, apiKeyID int64 }{userID, apiKeyID})
	}
}

func (s *OpenAIGatewayService) ValidateOpenAIHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) (bool, error) {
	if s == nil || strings.TrimSpace(responseID) == "" || userID <= 0 || apiKeyID <= 0 {
		return false, nil
	}
	ownerUser, ownerKey, found, err := s.getOpenAIWSStateStore().GetHTTPResponseOwner(ctx, groupID, responseID)
	if err != nil || !found {
		return false, err
	}
	return ownerUser == userID || (ownerUser <= 0 && ownerKey == apiKeyID), nil
}

func (s *OpenAIGatewayService) BindOpenAIHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) error {
	if s == nil {
		return nil
	}
	return s.getOpenAIWSStateStore().BindHTTPResponseOwner(ctx, groupID, responseID, userID, apiKeyID, s.openAIWSResponseStickyTTL())
}

func (s *OpenAIGatewayService) bindHTTPResponseAccount(ctx context.Context, c *gin.Context, account *Account, responseID string) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return
	}
	bindBaseCtx := context.Background()
	if ctx != nil {
		bindBaseCtx = context.WithoutCancel(ctx)
	}
	bindCtx, cancel := context.WithTimeout(bindBaseCtx, openAIWSStateStoreRedisTimeout)
	defer cancel()
	groupID := getOpenAIGroupIDFromContext(c)
	ttl := s.openAIWSResponseStickyTTL()
	logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, store.BindResponseAccount(bindCtx, groupID, responseID, account.ID, ttl))
	if owner, ok := c.Get(openAIHTTPResponseOwnerContextKeyCompat); ok {
		if binding, ok := owner.(struct{ userID, apiKeyID int64 }); ok {
			if err := store.BindHTTPResponseOwner(bindCtx, groupID, responseID, binding.userID, binding.apiKeyID, ttl); err != nil {
				logger.L().Warn("openai.http_bind_response_owner_failed", zap.Int64("group_id", groupID), zap.String("response_id", responseID), zap.Error(err))
			}
		}
	}
}

func openAIUsageFromGJSON(value gjson.Result) (OpenAIUsage, bool) {
	if !value.Exists() || !value.IsObject() {
		return OpenAIUsage{}, false
	}
	inputTokens := value.Get("input_tokens").Int()
	if inputTokens == 0 {
		inputTokens = value.Get("prompt_tokens").Int()
	}
	outputTokens := value.Get("output_tokens").Int()
	if outputTokens == 0 {
		outputTokens = value.Get("completion_tokens").Int()
	}
	if reasoning := firstPositiveGJSONInt(value.Get("output_tokens_details.reasoning_tokens"), value.Get("completion_tokens_details.reasoning_tokens")); reasoning > 0 {
		if total := value.Get("total_tokens").Int(); total > 0 && total == inputTokens+outputTokens+int64(reasoning) {
			outputTokens += int64(reasoning)
		}
	}
	cacheReadTokens := openAICacheReadTokensFromUsage(value)
	cacheCreationTokens := openAICacheCreationTokensFromUsage(value)
	cache30m, cache12h := openAICacheCreationTTLTokensFromUsage(value, cacheCreationTokens)
	imageOutputTokens := value.Get("output_tokens_details.image_tokens").Int()
	if imageOutputTokens == 0 {
		imageOutputTokens = value.Get("completion_tokens_details.image_tokens").Int()
	}
	imageInputTokens := firstPositiveGJSONInt(
		value.Get("input_tokens_details.image_tokens"),
		value.Get("prompt_tokens_details.image_tokens"),
	)
	return OpenAIUsage{
		InputTokens:                 int(inputTokens),
		ImageInputTokens:            imageInputTokens,
		OutputTokens:                int(outputTokens),
		CacheCreationInputTokens:    cacheCreationTokens,
		CacheCreation30mInputTokens: cache30m,
		CacheCreation12hInputTokens: cache12h,
		CacheReadInputTokens:        cacheReadTokens,
		ImageOutputTokens:           int(imageOutputTokens),
	}, true
}

func openAICacheReadTokensFromUsage(value gjson.Result) int {
	for _, nested := range []gjson.Result{
		value.Get("input_tokens_details.cached_tokens"),
		value.Get("prompt_tokens_details.cached_tokens"),
	} {
		if nested.Exists() {
			return max(int(nested.Int()), 0)
		}
	}

	return firstPositiveGJSONInt(
		value.Get("cache_read_input_tokens"),
		value.Get("cache_read_tokens"),
		value.Get("cached_tokens"),
		value.Get("prompt_cache_hit_tokens"), // DeepSeek：prompt_tokens = hit + miss
	)
}

func openAICacheCreationTokensFromUsage(value gjson.Result) int {
	for _, nested := range []gjson.Result{
		value.Get("input_tokens_details.cache_write_tokens"),
		value.Get("prompt_tokens_details.cache_write_tokens"),
		value.Get("input_tokens_details.cache_creation_tokens"),
		value.Get("prompt_tokens_details.cache_creation_tokens"),
	} {
		if nested.Exists() {
			return max(int(nested.Int()), 0)
		}
	}

	return firstPositiveGJSONInt(
		value.Get("cache_write_tokens"),
		value.Get("cache_creation_input_tokens"),
		value.Get("cache_write_input_tokens"),
		value.Get("cache_creation_tokens"),
	)
}

// Only reported usage determines the lifetime. Missing, partial or inconsistent
// details keep the aggregate's legacy price; request TTLs cannot prove writes.
func openAICacheCreationTTLTokensFromUsage(value gjson.Result, aggregate int) (int, int) {
	for _, path := range []string{"input_tokens_details", "prompt_tokens_details"} {
		details := value.Get(path + ".cache_write_tokens_details")
		if !details.Exists() {
			continue
		}
		short, long := details.Get("ttl_30m_tokens"), details.Get("ttl_12h_tokens")
		if short.Type != gjson.Number || long.Type != gjson.Number {
			return 0, 0
		}
		a, b := short.Int(), long.Int()
		if a < 0 || b < 0 || float64(a) != short.Float() || float64(b) != long.Float() ||
			a > int64(aggregate) || b != int64(aggregate)-a {
			return 0, 0
		}
		return int(a), int(b)
	}
	return 0, 0
}

func (s *OpenAIGatewayService) handleNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, mappedModel string) (*openaiNonStreamingResult, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	if bodyHasSSEFraming(body) {
		observeOpenAISSEBody(observer, string(body))
	} else {
		observer.ObserveOpenAI(body, strings.TrimSpace(gjson.GetBytes(body, "type").String()))
	}

	// Detect SSE responses for ALL account types via Content-Type header.
	// Some OpenAI-compatible upstreams (including other sub2api instances)
	// may return SSE even when stream=false was requested.
	if isEventStreamResponse(resp.Header) {
		return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
	}
	bodyLooksLikeSSE := bodyHasSSEFraming(body)

	// For OAuth accounts, also fall back to a body-content heuristic because
	// the upstream may omit the Content-Type header while still sending SSE.
	// This heuristic is NOT applied to API-key accounts to avoid false
	// positives on JSON responses that coincidentally contain "data:" or
	// "event:" in their text content.
	if account.Type == AccountTypeOAuth && bodyLooksLikeSSE {
		return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
	}
	if account != nil && account.IsGrok() && isOpenAIResponsesCompactPath(c) {
		body, err = convertGrokResponseToOpenAICompact(body)
		if err != nil {
			return nil, fmt.Errorf("convert Grok compact response: %w", err)
		}
	}

	usageValue, usageOK := extractOpenAIUsageFromJSONBytes(body)
	if !usageOK {
		if bodyLooksLikeSSE {
			return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
		}
		return nil, fmt.Errorf("parse response: invalid json response")
	}
	usage := &usageValue

	// Replace model in response if needed
	if originalModel != mappedModel {
		body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
	}
	body, err = restoreGrokResponsesClientToolPayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore Grok Responses client tool response: %w", err)
	}
	body, err = restoreOpenAIResponsesNamespacePayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI namespace response: %w", err)
	}
	if mapping, ok := openAIResponsesClientToolMapping(c); ok {
		body, _, err = apicompat.RestoreResponsesClientToolPayload(body, mapping)
		if err != nil {
			return nil, fmt.Errorf("restore OpenAI Responses client tools: %w", err)
		}
	}

	body = restoreCodexToolNamesFromContext(c, body)
	body = restoreOpenAIResponsesInstructionsEchoInResponse(c, body)
	body = ensureOpenAIResponseObjectCreatedAt(body)
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		imageCount:       countOpenAIResponseImageOutputsFromJSONBytes(body),
		imageOutputSizes: collectOpenAIResponseImageOutputSizesFromJSONBytes(body),
		searchCount:      countGrokNativeSearchCallsFromJSONBytes(body),
	}, nil
}

func isEventStreamResponse(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func bodyHasSSEFraming(body []byte) bool {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) handleSSEToJSON(resp *http.Response, c *gin.Context, account *Account, body []byte, originalModel, mappedModel string) (*openaiNonStreamingResult, error) {
	bodyText := string(body)
	finalResponse, ok := extractCodexFinalResponse(bodyText)
	terminalType, terminalPayload, terminalOK := extractOpenAISSETerminalEvent(bodyText)
	if terminalOK && (terminalType == "response.failed" || terminalType == "error") {
		ok = false
	}
	if !ok && terminalOK {
		finalResponse, ok = extractOpenAINonCompletedTerminalResponse(terminalType, terminalPayload)
	}

	usage := &OpenAIUsage{}
	if ok {
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse); parsed {
			*usage = parsedUsage
		}
		// When the terminal event has an empty output array, reconstruct
		// output from accumulated delta events so the client gets full content.
		// gjson Array() returns empty slice for null, missing, or empty arrays.
		if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
			if outputJSON, reconstructed := reconstructResponseOutputFromSSE(bodyText); reconstructed {
				if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
					finalResponse = patched
				}
			}
		}
		finalResponse = supplementCompactionItemFromSSE(c, finalResponse, bodyText)
		body = finalResponse
		if originalModel != mappedModel {
			body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
		}
		// Correct tool calls in final response
		body = s.correctToolCallsInResponseBody(body)
		restoredBody, restoreErr := restoreGrokResponsesClientToolPayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
		}
		restoredBody, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredBody)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
		}
		restoredBody = restoreCodexToolNamesFromContext(c, restoredBody)
		restoredBody = restoreOpenAIResponsesInstructionsEchoInResponse(c, restoredBody)
		body = ensureOpenAIResponseObjectCreatedAt(restoredBody)
	} else {
		// terminal event was handled above
		if terminalOK && (terminalType == "response.failed" || terminalType == "error") {
			msg := extractOpenAISSEErrorMessage(terminalPayload)
			if msg == "" {
				msg = "Upstream compact response failed"
			}
			if signal := newOpenAICompactFallbackSignal(c, terminalPayload, msg); signal != nil {
				return nil, signal
			}
			if failover := s.nonStreamingTerminalFailureFailover(c, resp, account, false, terminalType, terminalPayload, msg, mappedModel); failover != nil {
				return nil, failover
			}
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, msg)
		}
		usage = s.parseSSEUsageFromBody(bodyText)
		if originalModel != mappedModel {
			bodyText = s.replaceModelInSSEBody(bodyText, mappedModel, originalModel)
		}
		bodyText = restoreOpenAIResponsesInstructionsEchoInSSEBody(c, bodyText)
		body = []byte(bodyText)
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		// 上游的 Content-Type（text/event-stream）已复制到响应头，而 c.Data
		// 不会覆盖已存在的 Content-Type，这里需显式设置为聚合后的类型。
		c.Writer.Header().Set("Content-Type", contentType)
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		imageCount:       countOpenAIImageOutputsFromSSEBody(bodyText),
		imageOutputSizes: collectOpenAIImageOutputSizesFromSSEBody(bodyText),
		searchCount:      countGrokNativeSearchCallsFromSSEBody(bodyText),
	}, nil
}

func extractOpenAISSETerminalEvent(body string) (string, []byte, bool) {
	var terminalType string
	var terminalPayload []byte
	forEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		eventType = strings.TrimSpace(eventType)
		if eventType == "" {
			eventType = strings.TrimSpace(gjson.GetBytes(data, "type").String())
		}
		switch eventType {
		case "error", "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			terminalType = eventType
			terminalPayload = append([]byte(nil), data...)
		}
	})
	if terminalPayload != nil {
		return terminalType, terminalPayload, true
	}
	return "", nil, false
}

func extractOpenAISSEErrorMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if msg := strings.TrimSpace(gjson.GetBytes(payload, path).String()); msg != "" {
			return sanitizeUpstreamErrorMessage(msg)
		}
	}
	return sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
}

// buildOpenAIResponseFailedSSE synthesizes the response.failed terminal for a
// stream that ended on a bare error. created_at and sequence_number are always
// written: strict clients (grok-build, Codex) reject the frame without them.
func buildOpenAIResponseFailedSSE(responseID, model string, createdAt int64, source []byte, fallbackMessage string) string {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	errorType := strings.TrimSpace(gjson.GetBytes(source, "error.type").String())
	if errorType == "" {
		errorType = strings.TrimSpace(gjson.GetBytes(source, "response.error.type").String())
	}
	code := strings.TrimSpace(gjson.GetBytes(source, "error.code").String())
	if code == "" {
		code = strings.TrimSpace(gjson.GetBytes(source, "response.error.code").String())
	}
	if code == "" {
		code = "upstream_error"
	}
	message := extractOpenAISSEErrorMessage(source)
	if message == "" {
		message = strings.TrimSpace(fallbackMessage)
	}
	if message == "" {
		message = "Upstream response failed"
	}
	errorBody := gin.H{"code": code, "message": message}
	if errorType != "" {
		errorBody["type"] = errorType
	}
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	sequenceNumber := openAIResponsesFailedSequenceNumber(source)
	response := gin.H{
		"id":         responseID,
		"object":     "response",
		"created_at": createdAt,
		"status":     "failed",
		"output":     []any{},
		"error":      errorBody,
	}
	if model = strings.TrimSpace(model); model != "" {
		response["model"] = model
	}
	payload, err := marshalOpenAIUpstreamJSON(gin.H{
		"type":            "response.failed",
		"sequence_number": sequenceNumber,
		"response":        response,
	})
	if err != nil {
		// All values above are JSON primitives, so this is only a defensive fallback.
		payload = []byte(`{"type":"response.failed","sequence_number":` + strconv.FormatInt(sequenceNumber, 10) +
			`,"response":{"object":"response","created_at":` + strconv.FormatInt(createdAt, 10) +
			`,"status":"failed","output":[],"error":{"code":"upstream_error","message":"Upstream response failed"}}}`)
	}
	return "event: response.failed\ndata: " + string(payload) + "\n\n"
}

func sanitizeOpenAIResponseFailedEventForClient(payload []byte, eventType string, clientOutputStarted bool) ([]byte, bool) {
	eventType = strings.TrimSpace(eventType)
	isFailedEvent := eventType == "response.failed"
	if (!isFailedEvent && eventType != "error") || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
	if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(updated); changed {
		updated = rewritten
	}
	if !isFailedEvent {
		return updated, !bytes.Equal(updated, payload)
	}
	if clientOutputStarted && isOpenAIContextWindowError(extractOpenAISSEErrorMessage(payload), payload) {
		errorPath := ""
		if gjson.GetBytes(updated, "response.error").Exists() {
			errorPath = "response.error"
		} else if gjson.GetBytes(updated, "error").Exists() {
			errorPath = "error"
		}
		if errorPath != "" {
			next, err := sjson.SetBytes(updated, errorPath+".type", "invalid_request_error")
			if err != nil {
				return payload, false
			}
			updated = next
			next, err = sjson.SetBytes(updated, errorPath+".code", "context_length_exceeded")
			if err != nil {
				return payload, false
			}
			updated = next
		}
	}
	if !gjson.GetBytes(updated, "response").Exists() {
		return updated, !bytes.Equal(updated, payload)
	}
	for _, path := range []string{"response.instructions", "response.output", "response.usage", "response.metadata", "response.reasoning", "response.tools", "response.tool_choice", "response.parallel_tool_calls", "response.text", "response.truncation", "response.max_output_tokens", "response.incomplete_details"} {
		next, err := sjson.DeleteBytes(updated, path)
		if err != nil {
			return payload, false
		}
		updated = next
	}
	return updated, !bytes.Equal(updated, payload)
}

func (s *OpenAIGatewayService) writeOpenAINonStreamingProtocolError(resp *http.Response, c *gin.Context, message string) error {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "Upstream returned an invalid non-streaming response"
	}
	if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		writeOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", message)
		return fmt.Errorf("non-streaming openai protocol error: %s", message)
	}
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	return fmt.Errorf("non-streaming openai protocol error: %s", message)
}

// extractOpenAINonCompletedTerminalResponse 在没有 response.completed/done 时，
// 从 response.incomplete / response.cancelled 终止事件中取出 response 对象，
// 使非流式客户端拿到带 status/incomplete_details 的单个 JSON，而不是原始 SSE。
func extractOpenAINonCompletedTerminalResponse(terminalType string, payload []byte) ([]byte, bool) {
	switch terminalType {
	case "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return nil, false
	}
	if response := gjson.GetBytes(payload, "response"); response.Exists() && response.IsObject() {
		return []byte(response.Raw), true
	}
	return nil, false
}

func extractCodexFinalResponse(body string) ([]byte, bool) {
	var finalResponse []byte
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		if finalResponse != nil {
			return
		}
		if normalized, changed := normalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		eventType := gjson.GetBytes(data, "type").String()
		if eventType == "response.done" || eventType == "response.completed" {
			if response := gjson.GetBytes(data, "response"); response.Exists() && response.Type == gjson.JSON && response.Raw != "" {
				finalResponse = []byte(response.Raw)
			}
		}
	})
	if finalResponse != nil {
		return finalResponse, true
	}
	return nil, false
}

func normalizeCompletedImageGenerationStatus(data []byte) ([]byte, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data, false
	}

	shouldNormalize := func(item gjson.Result) bool {
		if !item.Exists() || !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != "image_generation_call" {
			return false
		}
		switch strings.TrimSpace(item.Get("status").String()) {
		case "generating", "in_progress":
			return strings.TrimSpace(item.Get("result").String()) != ""
		default:
			return false
		}
	}

	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.output_item.done":
		if !shouldNormalize(gjson.GetBytes(data, "item")) {
			return data, false
		}
		updated, err := sjson.SetBytes(data, "item.status", "completed")
		if err != nil {
			return data, false
		}
		return updated, true
	case "response.completed", "response.done":
		output := gjson.GetBytes(data, "response.output")
		if !output.Exists() || !output.IsArray() {
			return data, false
		}
		updated := data
		changed := false
		for i, item := range output.Array() {
			if !shouldNormalize(item) {
				continue
			}
			next, err := sjson.SetBytes(updated, "response.output."+strconv.Itoa(i)+".status", "completed")
			if err != nil {
				return data, false
			}
			updated = next
			changed = true
		}
		return updated, changed
	default:
		return data, false
	}
}

type responsesStreamOutputItems struct{ items map[int]json.RawMessage }

func newResponsesStreamOutputItems() *responsesStreamOutputItems {
	return &responsesStreamOutputItems{items: make(map[int]json.RawMessage)}
}

func (r *responsesStreamOutputItems) Observe(data []byte) {
	if r == nil || !gjson.ValidBytes(data) || gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(data, "item")
	if item.Exists() && item.IsObject() {
		r.items[int(gjson.GetBytes(data, "output_index").Int())] = json.RawMessage(append([]byte(nil), item.Raw...))
	}
}

func (r *responsesStreamOutputItems) HasItems() bool { return r != nil && len(r.items) > 0 }

func (r *responsesStreamOutputItems) Count() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}

func (r *responsesStreamOutputItems) BuildOutput() ([]byte, bool) {
	if !r.HasItems() {
		return nil, false
	}
	indexes := make([]int, 0, len(r.items))
	for index := range r.items {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, r.items[index])
	}
	data, err := json.Marshal(ordered)
	return data, err == nil
}

func normalizeResponsesStreamingTerminalOutput(data []byte, args ...any) ([]byte, bool) {
	var acc *apicompat.BufferedResponseAccumulator
	var imageOutputs []json.RawMessage
	var doneItems *responsesStreamOutputItems
	if len(args) == 2 {
		acc, _ = args[0].(*apicompat.BufferedResponseAccumulator)
		imageOutputs, _ = args[1].([]json.RawMessage)
	} else if len(args) >= 3 {
		acc, _ = args[0].(*apicompat.BufferedResponseAccumulator)
		doneItems, _ = args[1].(*responsesStreamOutputItems)
		imageOutputs, _ = args[2].([]json.RawMessage)
	}
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return data, false
	}

	output := gjson.GetBytes(data, "response.output")
	hasAccumulatedOutput := (acc != nil && acc.HasContent()) || len(imageOutputs) > 0 || (doneItems != nil && doneItems.HasItems())
	if output.Exists() && output.IsArray() {
		if len(output.Array()) > 0 {
			if doneItems == nil || !doneItems.HasItems() {
				return data, false
			}
			if doneOutput, ok := doneItems.BuildOutput(); ok && len(output.Array()) >= len(gjson.ParseBytes(doneOutput).Array()) {
				return data, false
			}
		}
		if len(output.Array()) == 0 && !hasAccumulatedOutput {
			return data, false
		}
	}

	outputJSON := []byte("[]")
	if doneItems != nil {
		if reconstructed, ok := doneItems.BuildOutput(); ok {
			outputJSON = reconstructed
		}
	} else if reconstructed, ok := buildResponsesOutputJSON(acc, imageOutputs); ok {
		outputJSON = reconstructed
	}
	updated, err := sjson.SetRawBytes(data, "response.output", outputJSON)
	if err != nil {
		return data, false
	}
	return updated, true
}

func responsesStreamEventMayContributeToOutput(eventType string) bool {
	switch eventType {
	case "response.output_text.delta",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta":
		return true
	default:
		return false
	}
}

// collectRawResponsesOutputItemsFromSSE preserves raw output_item.done items.
// Raw items are the authoritative final Responses shape and can contain compact
// fields that narrow structs do not know yet.
func collectRawResponsesOutputItemsFromSSE(bodyText string) ([]byte, bool) {
	var items []json.RawMessage
	seen := make(map[string]struct{})
	hasCompactionItem := false
	appendItem := func(item gjson.Result) {
		if !item.Exists() || !item.IsObject() {
			return
		}
		key := strings.TrimSpace(item.Get("id").String())
		if key == "" {
			key = item.Raw
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		if isResponsesCompactionItemType(item.Get("type").String()) {
			hasCompactionItem = true
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
		if normalized, changed := normalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
			return
		}
		appendItem(gjson.GetBytes(data, "item"))
	})
	if !hasCompactionItem {
		forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
			if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.added" {
				return
			}
			item := gjson.GetBytes(data, "item")
			if !isResponsesCompactionItemType(item.Get("type").String()) {
				return
			}
			appendItem(item)
		})
	}
	if len(items) == 0 {
		return nil, false
	}
	outputJSON, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

func isResponsesCompactionItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

func supplementCompactionItemFromSSE(c *gin.Context, finalResponse []byte, bodyText string) []byte {
	if !isOpenAIResponsesCompactPath(c) {
		return finalResponse
	}
	if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
		return finalResponse
	}
	if responsesOutputHasCompactionItem(finalResponse) {
		return finalResponse
	}
	item, found := findRawCompactionItemFromSSE(bodyText)
	if !found {
		return finalResponse
	}
	patched, err := sjson.SetRawBytes(finalResponse, "output.-1", item)
	if err != nil {
		return finalResponse
	}
	return patched
}

func responsesOutputHasCompactionItem(response []byte) bool {
	for _, item := range gjson.GetBytes(response, "output").Array() {
		if isResponsesCompactionItemType(item.Get("type").String()) {
			return true
		}
	}
	return false
}

func findRawCompactionItemFromSSE(bodyText string) (json.RawMessage, bool) {
	var found json.RawMessage
	pick := func(eventType string) {
		forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
			if found != nil {
				return
			}
			if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != eventType {
				return
			}
			item := gjson.GetBytes(data, "item")
			if !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
				return
			}
			found = json.RawMessage(item.Raw)
		})
	}
	pick("response.output_item.done")
	if found == nil {
		pick("response.output_item.added")
	}
	return found, found != nil
}

// reconstructResponseOutputFromSSE scans raw SSE body text for delta events and
// returns a JSON-encoded output array reconstructed from accumulated deltas.
// Returns (nil, false) if no content was found in deltas.
func reconstructResponseOutputFromSSE(bodyText string) ([]byte, bool) {
	if outputJSON, ok := collectRawResponsesOutputItemsFromSSE(bodyText); ok {
		return outputJSON, true
	}
	acc := apicompat.NewBufferedResponseAccumulator()
	imageOutputs := make([]json.RawMessage, 0, 1)
	seenImages := make(map[string]struct{})
	forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
		if imageOutput, ok := extractImageGenerationOutputFromSSEData(data, seenImages); ok {
			imageOutputs = append(imageOutputs, imageOutput)
		}
		eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		if responsesStreamEventMayContributeToOutput(eventType) {
			var event apicompat.ResponsesStreamEvent
			if err := json.Unmarshal(data, &event); err == nil {
				acc.ProcessEvent(&event)
			}
		}
	})
	return buildResponsesOutputJSON(acc, imageOutputs)
}

func buildResponsesOutputJSON(acc *apicompat.BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	if (acc == nil || !acc.HasContent()) && len(imageOutputs) == 0 {
		return nil, false
	}
	var output []json.RawMessage
	if acc != nil && acc.HasContent() {
		outputJSON, err := json.Marshal(acc.BuildOutput())
		if err == nil {
			_ = json.Unmarshal(outputJSON, &output)
		}
	}
	output = append(output, imageOutputs...)
	if len(output) == 0 {
		return nil, false
	}

	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

func extractImageGenerationOutputFromSSEData(data []byte, seen map[string]struct{}) (json.RawMessage, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil, false
	}
	if gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return nil, false
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() || item.Get("type").String() != "image_generation_call" {
		return nil, false
	}
	if strings.TrimSpace(item.Get("result").String()) == "" {
		return nil, false
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("output_format").String()) + "|" + strings.TrimSpace(item.Get("result").String())
	}
	if key != "" && seen != nil {
		if _, exists := seen[key]; exists {
			return nil, false
		}
		seen[key] = struct{}{}
	}
	return json.RawMessage(item.Raw), true
}

func (s *OpenAIGatewayService) parseSSEUsageFromBody(body string) *OpenAIUsage {
	usage := &OpenAIUsage{}
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		s.parseSSEUsageBytes(data, usage)
	})
	return usage
}

func (s *OpenAIGatewayService) replaceModelInSSEBody(body, fromModel, toModel string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if _, ok := extractOpenAISSEDataLine(line); !ok {
			continue
		}
		lines[i] = s.replaceModelInSSELine(line, fromModel, toModel)
	}
	return strings.Join(lines, "\n")
}
