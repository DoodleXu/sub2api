package admin

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ImageGenerationHandler provides a read-only view of the upstream async image bucket.
type ImageGenerationHandler struct {
	settings *service.ImageStorageSettingService
	factory  service.ImageStorageBrowserFactory
}

func NewImageGenerationHandler(settings *service.ImageStorageSettingService, factory service.ImageStorageBrowserFactory) *ImageGenerationHandler {
	return &ImageGenerationHandler{settings: settings, factory: factory}
}

func (h *ImageGenerationHandler) List(c *gin.Context) {
	if h == nil || h.settings == nil || h.factory == nil {
		response.BadRequest(c, "async image object storage is unavailable")
		return
	}
	cfg, err := h.settings.BrowserConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	prefix := strings.TrimSpace(c.Query("prefix"))
	if prefix == "" {
		prefix = cfg.Prefix
	} else if !strings.HasPrefix(prefix, cfg.Prefix) {
		response.BadRequest(c, "prefix must stay within the configured async image prefix")
		return
	}
	browser, err := h.factory(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	page, err := browser.List(c.Request.Context(), prefix, c.Query("cursor"), intQuery(c, "limit", 60))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"items":       page.Items,
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
		"prefix":      cfg.Prefix,
		"bucket":      cfg.Bucket,
	})
}

func intQuery(c *gin.Context, key string, fallback int) int {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
