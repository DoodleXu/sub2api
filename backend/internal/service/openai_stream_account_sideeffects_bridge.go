package service

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func openAIStream403AccountFailure(payload []byte, message string) bool {
	return isOpenAIUpstreamAccessStateError(message, payload) || openAIStreamCredentialAuthFailure(payload)
}

func openAIStreamCredentialAuthFailure(payload []byte) bool {
	if len(bytes.TrimSpace(payload)) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"response.error.status_code", "error.status_code", "status_code"} {
		if int(gjson.GetBytes(payload, path).Int()) == http.StatusUnauthorized {
			return true
		}
	}
	for _, path := range []string{"response.error.code", "error.code", "code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "invalid_api_key", "api_key_disabled", "unauthorized", "authentication_error", "invalid_token", "access_token_invalid", "token_revoked", "token_invalidated", "invalid_credentials", "credential_invalid":
			return true
		}
	}
	for _, path := range []string{"response.error.type", "error.type", "type"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "authentication_error", "authentication_failed", "unauthorized_error":
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) handleOpenAIStreamTerminalAccountSideEffects(c *gin.Context, account *Account, payload []byte, message string, headers http.Header) (int, bool) {
	statusCode := openAIStreamFailureStatus(payload, message)
	switch statusCode {
	case http.StatusForbidden:
		if !openAIStream403AccountFailure(payload, message) {
			return statusCode, false
		}
		fallthrough
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		if statusCode == http.StatusTooManyRequests {
			headers = nil
		}
		return statusCode, s.handleOpenAIAccountUpstreamError(ctx, account, statusCode, headers, payload)
	default:
		return statusCode, false
	}
}
