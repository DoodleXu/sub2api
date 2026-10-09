package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) ListEmailBroadcastTemplates(c *gin.Context) {
	items, err := h.notificationEmailService.ListBroadcastContentTemplates(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, items)
}

func (h *SettingHandler) SaveEmailBroadcastTemplate(c *gin.Context) {
	var req service.BroadcastContentTemplate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.notificationEmailService.SaveBroadcastContentTemplate(c.Request.Context(), c.Param("template_id"), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, item)
}

func (h *SettingHandler) DeleteEmailBroadcastTemplate(c *gin.Context) {
	if err := h.notificationEmailService.DeleteBroadcastContentTemplate(c.Request.Context(), c.Param("template_id")); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func (h *SettingHandler) PreviewEmailBroadcastContent(c *gin.Context) {
	var req service.BroadcastContent
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	preview, err := h.notificationEmailService.PreviewBroadcastContent(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, preview)
}
