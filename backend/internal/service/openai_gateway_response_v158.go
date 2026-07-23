package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openaiStreamingResult streaming response result
func (s *OpenAIGatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel string) (*openaiStreamingResult, error) {
	return s.handleStreamingResponseWithReasoning(ctx, resp, c, account, startTime, originalModel, mappedModel, "")
}

func (s *OpenAIGatewayService) handleStreamingResponseWithReasoning(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel, reasoningEffort string) (*openaiStreamingResult, error) {
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
	var attemptResponseHeaders http.Header
	if guardFirstOutput {
		if s.responseHeaderFilter != nil {
			attemptResponseHeaders = responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter)
		} else if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
			attemptResponseHeaders = http.Header{"X-Request-Id": []string{requestID}}
		}
	} else if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if !guardFirstOutput && resp.Header.Get("x-request-id") != "" {
		v := resp.Header.Get("x-request-id")
		c.Header("x-request-id", v)
	}
	applyAttemptResponseHeaders := func() {
		if !guardFirstOutput || len(attemptResponseHeaders) == 0 || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
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
	bufferedWriter := bufio.NewWriterSize(w, 4*1024)
	var firstOutputStage *openAIFirstOutputStage
	if guardFirstOutput {
		firstOutputStage = newDefaultOpenAIFirstOutputStage()
		defer func() {
			if err := firstOutputStage.Close(); err != nil {
				logger.LegacyPrintf("service.openai_gateway", "OpenAI first-output staging cleanup failed: account=%d model=%s error=%v", account.ID, originalModel, err)
			}
		}()
	}
	writePendingString := func(value string) (int, error) {
		if firstOutputStage != nil && firstTokenMs == nil && !firstOutputStage.closed {
			return firstOutputStage.WriteString(value)
		}
		return bufferedWriter.WriteString(value)
	}
	pendingBytes := func() int64 {
		if firstOutputStage != nil && firstTokenMs == nil && !firstOutputStage.closed {
			return firstOutputStage.Buffered()
		}
		return int64(bufferedWriter.Buffered())
	}
	flushBuffered := func() error {
		if firstOutputStage != nil && firstTokenMs == nil && !firstOutputStage.closed {
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
	firstOutputScanGuard.Store(guardFirstOutput)
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	if guardFirstOutput {
		scanner.Split(openAIFirstOutputDynamicScanLines(&firstOutputScanGuard))
	}
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	// 仅监控上游数据间隔超时，不被下游写入阻塞影响
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
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
	failedMessage := ""
	clientOutputStarted := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	var streamEarlyErr error
	eventInProgress := false
	eventStartsClientOutput := false
	eventShouldFlush := false
	handlePendingWriteError := func(err error) {
		if firstOutputStage != nil && firstTokenMs == nil && !firstOutputStage.closed {
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
		completedSemanticEvent := eventStartsClientOutput
		shouldFlush := eventShouldFlush || (queueDrained && clientOutputStarted)
		eventInProgress = false
		if !clientDisconnected {
			if completedSemanticEvent {
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
		if completedSemanticEvent && firstTokenMs == nil {
			firstOutputScanGuard.Store(false)
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
			stopFirstOutputTimer()
		}
		eventStartsClientOutput = false
		eventShouldFlush = false
	}
	sendErrorEvent := func(reason string) {
		if errorEventSent || clientDisconnected {
			return
		}
		errorEventSent = true
		payload := `{"type":"error","sequence_number":0,"error":{"type":"upstream_error","message":` + strconv.Quote(reason) + `,"code":` + strconv.Quote(reason) + `}}`
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		if _, err := writePendingString("data: " + payload + "\n\n"); err != nil {
			clientDisconnected = true
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
		lastDownstreamWriteAt = time.Now()
	}

	needModelReplace := originalModel != mappedModel
	streamOutputAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	resultWithUsage := func() *openaiStreamingResult {
		return &openaiStreamingResult{
			usage:              usage,
			firstTokenMs:       firstTokenMs,
			imageFirstOutputMs: imageFirstOutputMs,
			responseID:         responseID,
			imageCount:         imageCounter.Count(),
			imageOutputSizes:   imageCounter.Sizes(),
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
		lastDownstreamWriteAt = time.Now()
	}
	finalizeStream := func() (*openaiStreamingResult, error) {
		if guardFirstOutput && eventInProgress {
			// EOF dispatches the final SSE event even without a trailing blank line.
			completeGuardedEvent(true)
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
		if errors.Is(scanErr, errOpenAIFirstOutputScannerLimit) && firstTokenMs == nil {
			logger.LegacyPrintf("service.openai_gateway", "SSE token exceeded guarded first-output limit: account=%d limit=%d error=%v", account.ID, openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance, safeScanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) && guardFirstOutput && firstTokenMs == nil {
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
				logger.LegacyPrintf("service.openai_gateway", "Upstream scan ended after terminal event: %v", safeScanErr)
			}
			result, err := finalizeStream()
			return result, err, true
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
		sendErrorEvent("stream_read_error")
		return resultWithUsage(), fmt.Errorf("stream read error: %w", safeScanErr), true
	}
	processSSELine := func(line string, queueDrained bool) {
		if streamEarlyErr != nil {
			return
		}
		// Extract data from SSE line (supports both "data: " and "data:" formats)
		if data, ok := extractOpenAISSEDataLine(line); ok {
			dataBytes := []byte(data)
			eventTypeRaw := gjson.GetBytes(dataBytes, "type").String()
			eventType := strings.TrimSpace(eventTypeRaw)
			if isOpenAIErrorBearingEventType(eventType) {
				if redacted := redactSensitiveBody(dataBytes); !bytes.Equal(redacted, dataBytes) {
					dataBytes = redacted
					data = string(redacted)
					line = "data: " + data
				}
			}
			// 初始上游 data 的 type 只解析一次：原始值用于终止事件精确匹配，规范化值供后续分支复用。
			if openAIStreamEventIsTerminalWithType(data, eventTypeRaw) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			forceFlushFailedEvent := false
			if eventType == "response.failed" {
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				// response.failed 自带上游已消耗的 usage（input token 通常已扣）；必须先解析
				// 再打 cyber 标记，否则 mark 记到的是解析前的 0，导致流式 cyber 按 0 token 计费
				// 而漏记真实用量。对齐 WS V2 / Chat 流式路径（均先解析 usage 再 Mark）。
				s.parseSSEUsageBytes(dataBytes, usage)
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
						streamEarlyErr = s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, dataBytes, failedMessage)
						return
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
			}
			if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
			}
			imageCounter.AddSSEData(dataBytes)
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
			if responsesStreamEventMayContributeToOutput(eventType) {
				var streamEvent apicompat.ResponsesStreamEvent
				if err := json.Unmarshal(dataBytes, &streamEvent); err == nil {
					streamOutputAccumulator.ProcessEvent(&streamEvent)
				}
			}
			if normalizedData, normalized := normalizeResponsesStreamingTerminalOutput(dataBytes, streamOutputAccumulator, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			restoredData, restoreErr := restoreOpenAIResponsesNamespacePayload(c, dataBytes)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
				return
			}
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
			// Replace model in response if needed.
			// Fast path: most events do not contain model field values.
			if needModelReplace && mappedModel != "" && strings.Contains(line, mappedModel) {
				line = s.replaceModelInSSELine(line, mappedModel, originalModel)
			}
			startsClientOutput := forceFlushFailedEvent || openAIStreamDataStartsClientOutput(data, eventType)
			if guardFirstOutput {
				eventStartsClientOutput = eventStartsClientOutput || startsClientOutput
			}

			// 写入客户端（客户端断开后继续 drain 上游）
			if !clientDisconnected {
				shouldFlush := queueDrained && (clientOutputStarted || startsClientOutput)
				if firstTokenMs == nil && startsClientOutput {
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
			if !guardFirstOutput && firstTokenMs == nil && startsClientOutput {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
				stopFirstOutputTimer()
			}
			s.parseSSEUsageBytes(dataBytes, usage)
			return
		}

		// A blank line dispatches a guarded event from the attempt-local stage.
		if guardFirstOutput && line == "" {
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
	sendEvent := func(ev scanEvent) bool {
		if guardFirstOutput {
			ev.processed = make(chan struct{})
		}
		select {
		case events <- ev:
		case <-done:
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
			if !ok {
				if guardFirstOutput && eventInProgress {
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

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.LegacyPrintf("service.openai_gateway", "Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-firstOutputCh:
			if firstTokenMs != nil {
				stopFirstOutputTimer()
				continue
			}
			_ = resp.Body.Close()
			for ev := range events {
				markEventProcessed(ev)
			}
			return resultWithUsage(), s.newOpenAIFirstOutputTimeoutError(
				ctx, c, account, startTime, originalModel, reasoningEffort,
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
			if guardFirstOutput {
				// Bypass attempt-local buffered frames. The stable SSE headers may be
				// committed here, but account headers remain private until semantic output.
				if _, err := w.Write([]byte(":\n\n")); err != nil {
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

func openAIStreamFailedEventSemanticStatus(payload []byte, message string) int {
	if isOpenAIContextWindowError(message, payload) {
		return http.StatusBadRequest
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	combined := strings.TrimSpace(errType + " " + code + " " + strings.ToLower(strings.TrimSpace(message)))
	switch {
	case strings.Contains(errType, "invalid_request"):
		return http.StatusBadRequest
	case strings.Contains(combined, "rate_limit"):
		return http.StatusTooManyRequests
	case strings.Contains(combined, "authentication") || strings.Contains(combined, "unauthorized") || strings.Contains(combined, "invalid_api_key"):
		return http.StatusUnauthorized
	case strings.Contains(combined, "permission") || strings.Contains(combined, "forbidden") || strings.Contains(combined, "access denied"):
		return http.StatusForbidden
	case code == "server_is_overloaded" || code == "slow_down":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func openAIStreamFailedEventPassthroughBody(payload []byte, failedMessage string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || gjson.GetBytes(payload, "error").Exists() {
		return payload
	}
	responseError := gjson.GetBytes(payload, "response.error")
	if !responseError.Exists() {
		if strings.TrimSpace(failedMessage) == "" {
			return payload
		}
		body, err := marshalOpenAIUpstreamJSON(gin.H{"error": gin.H{"message": failedMessage}})
		if err != nil {
			return payload
		}
		return body
	}
	errorPayload := gin.H{}
	for _, key := range []string{"type", "code", "param"} {
		if value := strings.TrimSpace(gjson.Get(responseError.Raw, key).String()); value != "" {
			errorPayload[key] = value
		}
	}
	message := strings.TrimSpace(gjson.Get(responseError.Raw, "message").String())
	if message == "" {
		message = strings.TrimSpace(failedMessage)
	}
	if message != "" {
		errorPayload["message"] = message
	}
	if len(errorPayload) == 0 {
		return payload
	}
	body, err := marshalOpenAIUpstreamJSON(gin.H{"error": errorPayload})
	if err != nil {
		return payload
	}
	return body
}

func applyOpenAIStreamFailedErrorPassthroughRule(c *gin.Context, platform string, payload []byte, failedMessage string) (status int, errType string, errMsg string, matched bool) {
	return applyErrorPassthroughRule(c, platform, openAIStreamFailedEventSemanticStatus(payload, failedMessage), openAIStreamFailedEventPassthroughBody(payload, failedMessage), http.StatusBadGateway, "upstream_error", "Upstream request failed")
}

func sanitizeOpenAIResponseFailedEventForClient(payload []byte, eventType string, clientOutputStarted bool) ([]byte, bool) {
	if eventType != "response.failed" || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
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

// isOpenAIErrorBearingEventType 标识可能携带上游错误明细、需先脱敏再记录或转发的事件。
func isOpenAIErrorBearingEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "error", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}
