package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type DailyCheckinHandler struct {
	dailyCheckinService *service.DailyCheckinService
}

func NewDailyCheckinHandler(dailyCheckinService *service.DailyCheckinService) *DailyCheckinHandler {
	return &DailyCheckinHandler{dailyCheckinService: dailyCheckinService}
}

// Status returns current user's daily check-in status.
func (h *DailyCheckinHandler) Status(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	status, err := h.dailyCheckinService.GetStatus(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// CheckIn grants the configured random reward when today's usage is eligible.
func (h *DailyCheckinHandler) CheckIn(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	result, err := h.dailyCheckinService.CheckIn(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
