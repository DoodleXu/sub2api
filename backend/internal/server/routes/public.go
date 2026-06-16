package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// RegisterPublicRoutes registers signed, unauthenticated routes that carry
// their own authorization token in the request URL.
func RegisterPublicRoutes(v1 *gin.RouterGroup, h *handler.Handlers) {
	v1.GET("/image-assets/:asset_id", h.WebConsoleImageTask.GetSignedAsset)
}
