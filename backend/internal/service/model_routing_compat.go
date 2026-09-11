package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

func modelRoutingAppliesToPlatform(target, group string) bool {
	target = strings.TrimSpace(target)
	group = strings.TrimSpace(group)
	if target != PlatformOpenAI && target != PlatformAnthropic {
		return false
	}
	if group == PlatformComposite {
		return true
	}
	return group == target
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
