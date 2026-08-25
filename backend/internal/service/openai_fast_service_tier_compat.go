package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

type ErrInvalidOpenAIServiceTier struct{ Value string }

func (e *ErrInvalidOpenAIServiceTier) Error() string {
	return fmt.Sprintf("invalid service_tier %q; allowed values include fast, priority, flex, auto, default, scale", e.Value)
}

func ValidateOpenAIServiceTierField(body []byte) (string, error) {
	v := gjson.GetBytes(body, "service_tier")
	if !v.Exists() || v.Type == gjson.Null {
		return "", nil
	}
	if v.Type != gjson.String {
		return "", &ErrInvalidOpenAIServiceTier{Value: "<non-string>"}
	}
	raw := strings.TrimSpace(v.String())
	value := strings.ToLower(raw)
	if value == "fast" {
		return "priority", nil
	}
	switch value {
	case "priority", "flex", "auto", "default", "scale":
		return value, nil
	default:
		if len(value) > 64 {
			value = value[:64] + "..."
		}
		return "", &ErrInvalidOpenAIServiceTier{Value: value}
	}
}
