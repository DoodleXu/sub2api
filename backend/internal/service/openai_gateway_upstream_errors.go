package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

func openAIRequestScopedRetryDelay(responseHeaders http.Header, responseBody []byte) time.Duration {
	type retryAfterValue struct {
		raw          string
		milliseconds bool
	}
	for _, value := range []retryAfterValue{
		{raw: responseHeaders.Get("Retry-After")},
		{raw: responseHeaders.Get("Retry-After-Ms"), milliseconds: true},
		{raw: gjson.GetBytes(responseBody, "error.headers.retry-after").String()},
		{raw: gjson.GetBytes(responseBody, "response.error.headers.retry-after").String()},
		{raw: gjson.GetBytes(responseBody, "error.headers.retry-after-ms").String(), milliseconds: true},
		{raw: gjson.GetBytes(responseBody, "response.error.headers.retry-after-ms").String(), milliseconds: true},
	} {
		raw := strings.TrimSpace(value.raw)
		if raw == "" {
			continue
		}
		var delay time.Duration
		var ok bool
		if value.milliseconds {
			delay, ok = parseOpenAIRequestScopedRetryDelayMilliseconds(raw)
		} else {
			delay, ok = parseOpenAIRequestScopedRetryDelay(raw)
		}
		if !ok || delay <= 0 {
			continue
		}
		// Preserve the provider minimum. The handler declines delays beyond its
		// retry budget; truncating here would make it retry too early.
		return delay
	}
	return 0
}

func parseOpenAIRequestScopedRetryDelay(raw string) (time.Duration, bool) {
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second)), true
	}
	if resetAt, err := http.ParseTime(raw); err == nil {
		delay := time.Until(resetAt)
		if delay > 0 {
			return delay, true
		}
	}
	return 0, false
}

func parseOpenAIRequestScopedRetryDelayMilliseconds(raw string) (time.Duration, bool) {
	if millis, err := strconv.ParseFloat(raw, 64); err == nil && millis > 0 {
		return time.Duration(millis * float64(time.Millisecond)), true
	}
	return 0, false
}
