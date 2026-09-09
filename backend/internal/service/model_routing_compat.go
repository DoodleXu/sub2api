package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

func modelRoutingAppliesToPlatform(target, group string) bool {
	return target == "" || group == "" || target == group
}

func isOpenAICompatibleModelNotFound400(body []byte) bool {
	code := strings.TrimSpace(extractUpstreamErrorCode(body))
	if code != "" {
		return strings.EqualFold(code, "model_not_found")
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if message == "" && !gjson.ValidBytes(body) {
		message = strings.ToLower(strings.TrimSpace(string(body)))
	}
	return strings.Contains(message, "unknown provider for model") ||
		strings.Contains(message, "model not found") ||
		strings.Contains(message, "model is not supported")
}

func IsOpenAICompatibleModelNotFound400(body []byte) bool {
	return isOpenAICompatibleModelNotFound400(body)
}

func containsFold(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
