package service

import (
	"net/http"
)

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
	if account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode) {
		return true
	}
	switch statusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}
