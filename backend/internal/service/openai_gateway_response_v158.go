package service

import (
	"strings"
)

// isOpenAIErrorBearingEventType 标识可能携带上游错误明细、需先脱敏再记录或转发的事件。
func isOpenAIErrorBearingEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "error", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}
