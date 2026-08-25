package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func openAIAccountScheduleModel(c *gin.Context, account *service.Account, forwardModel string, requireCompact bool, result *service.OpenAIForwardResult) string {
	if result != nil && strings.TrimSpace(result.UpstreamModel) != "" {
		return strings.TrimSpace(result.UpstreamModel)
	}
	if c != nil {
		if value, ok := c.Get(service.OpsUpstreamModelKey); ok {
			if actual, ok := value.(string); ok && strings.TrimSpace(actual) != "" {
				return strings.TrimSpace(actual)
			}
		}
	}
	return service.ResolveOpenAIAccountUpstreamModelForRequest(account, forwardModel, requireCompact)
}
