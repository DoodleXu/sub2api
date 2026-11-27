package service

import (
	"strings"
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
