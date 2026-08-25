package service

import "net/http"

func shouldFailoverOpenAIPassthroughResponse(account *Account, statusCode int, body []byte) bool {
	if hit, _, _ := detectOpenAICyberPolicy(body); hit || isOpenAIContextWindowError("", body) {
		return false
	}
	if isOpenAIHTTPUpstreamAccessStateError(statusCode, "", body) ||
		isOpenAIRequestBodyTooLargeError(statusCode, "", body) {
		return true
	}
	if account != nil && account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode) {
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
