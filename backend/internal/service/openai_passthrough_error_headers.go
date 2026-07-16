package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func writeOpenAIPassthroughErrorHeaders(dst, src http.Header) {
	if dst == nil {
		return
	}
	dst.Set("Content-Type", "application/json; charset=utf-8")
	dst.Set("Cache-Control", "no-store")
	dst.Del("Retry-After")
	if src == nil {
		return
	}
	raw := strings.TrimSpace(src.Get("Retry-After"))
	if validOpenAIPassthroughRetryAfter(raw, time.Now()) {
		dst.Set("Retry-After", raw)
	}
}

func validOpenAIPassthroughRetryAfter(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	digits := true
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		return err == nil && seconds > 0
	}
	parsed, err := http.ParseTime(raw)
	return err == nil && parsed.After(now)
}

func shouldFailoverOpenAIPassthroughResponseV158(account *Account, statusCode int, responseBody []byte) bool {
	if isOpenAIContextWindowError("", responseBody) {
		return false
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, "", responseBody) {
		return true
	}
	switch statusCode {
	case http.StatusTooManyRequests, 529:
		return true
	}
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	switch statusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}

func writeSanitizedOpenAIPassthroughError(c *gin.Context, upstreamStatus int, upstreamHeaders http.Header) {
	downstreamStatus := upstreamStatus
	message := "Upstream request failed"
	switch upstreamStatus {
	case http.StatusUnauthorized:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream authentication failed"
	case http.StatusForbidden:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream access denied"
	default:
		if upstreamStatus >= http.StatusInternalServerError {
			message = "Upstream service temporarily unavailable"
		}
	}
	writeOpenAIPassthroughErrorEnvelope(c, downstreamStatus, upstreamHeaders, message)
}

func writeOpenAIPassthroughErrorEnvelope(c *gin.Context, downstreamStatus int, upstreamHeaders http.Header, message string) {
	if c == nil {
		return
	}
	body, _ := json.Marshal(gin.H{"error": gin.H{"type": "upstream_error", "message": message}})
	if writeOpenAICompactSSEBridge(c, downstreamStatus, body) {
		return
	}
	writeOpenAIPassthroughErrorHeaders(c.Writer.Header(), upstreamHeaders)
	c.Data(downstreamStatus, "application/json; charset=utf-8", body)
}

func (s *OpenAIGatewayService) handleFailoverErrorResponsePassthroughV158(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	responseBody []byte,
) error {
	body := s.redactAgentIdentitySensitiveBody(ctx, account, responseBody)
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
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
	reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
	canonicalModel := canonicalOpenAIAccountSchedulingModel(account, reqModel)
	_ = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, canonicalModel)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
		Passthrough: true, Kind: "failover", Message: upstreamMsg, Detail: upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	return newOpenAIUpstreamFailoverError(resp.StatusCode, resp.Header, body, upstreamMsg,
		account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode))
}

func (s *OpenAIGatewayService) handleErrorResponsePassthroughV158(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	responseBody []byte,
) error {
	MarkResponseCommitted(c)
	body := s.redactAgentIdentitySensitiveBody(ctx, account, responseBody)
	cyberHit, cyberCode, cyberMsg := detectOpenAICyberPolicy(body)
	if cyberHit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{Code: cyberCode, Message: cyberMsg,
			Body: truncateString(string(body), 4096), UpstreamStatus: resp.StatusCode})
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
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
	if !cyberHit {
		reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
		canonicalModel := canonicalOpenAIAccountSchedulingModel(account, reqModel)
		_ = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, canonicalModel)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
		UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
		Passthrough: true, Kind: "http_error", Message: upstreamMsg, Detail: upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	if isOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		writeOpenAIPassthroughErrorEnvelope(c, resp.StatusCode, resp.Header, upstreamMsg)
	} else {
		writeSanitizedOpenAIPassthroughError(c, resp.StatusCode, resp.Header)
	}
	return fmt.Errorf("upstream error: %d (client response sanitized)", resp.StatusCode)
}

const OpenAIRequestBodyTooLargeClientMessage = "Request payload is too large"

const openAIRequestBodyTooLargeReason = GatewayFailureReason("openai_request_body_too_large")

func isOpenAIRequestBodyTooLargeError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	return statusCode == http.StatusRequestEntityTooLarge && !isOpenAIContextWindowError(upstreamMsg, upstreamBody)
}

func newOpenAIUpstreamFailoverError(statusCode int, responseHeaders http.Header, responseBody []byte, upstreamMsg string, retryableOnSameAccount bool) *UpstreamFailoverError {
	failoverErr := &UpstreamFailoverError{
		StatusCode: statusCode, ResponseBody: responseBody, ResponseHeaders: responseHeaders.Clone(),
		RetryableOnSameAccount: retryableOnSameAccount,
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameAccount = false
		failoverErr.Scope = GatewayFailureScopeAccount
		failoverErr.Reason = openAIRequestBodyTooLargeReason
		failoverErr.NextAccountAction = NextAccountRetry
		failoverErr.ClientStatusCode = http.StatusRequestEntityTooLarge
		failoverErr.ClientMessage = OpenAIRequestBodyTooLargeClientMessage
	}
	return failoverErr
}

func (e *UpstreamFailoverError) IsOpenAIRequestBodyTooLarge() bool {
	return e != nil && e.Reason == openAIRequestBodyTooLargeReason
}
