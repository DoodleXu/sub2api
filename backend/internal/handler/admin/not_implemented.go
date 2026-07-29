package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// respondStatsNotImplemented keeps legacy routes explicit without returning
// fabricated zero-value metrics that can mislead operators.
func respondStatsNotImplemented(c *gin.Context) {
	response.ErrorWithDetails(
		c,
		http.StatusNotImplemented,
		"statistics endpoint is not implemented",
		"STATS_NOT_IMPLEMENTED",
		nil,
	)
}
