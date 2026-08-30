//go:build unit

package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuthRoutesRegisterDesktopCheckoutSessionPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	noopJWT := servermiddleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() })
	noopAudit := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAuthRoutes(v1, &handler.Handlers{
		Auth:            &handler.AuthHandler{},
		Setting:         &handler.SettingHandler{},
		DesktopCheckout: handler.NewDesktopCheckoutHandler(nil),
	}, noopJWT, noopAudit, nil, nil, nil)

	paths := map[string]bool{}
	for _, route := range router.Routes() {
		if route.Path == "/api/v1/desktop/checkout-sessions" || route.Path == "/api/v1/desktop/checkout-sessions/:session_id" || route.Path == "/api/v1/desktop/checkout-sessions/:session_id/activate" {
			paths[route.Method+" "+route.Path] = true
		}
	}
	require.True(t, paths[http.MethodPost+" /api/v1/desktop/checkout-sessions"])
	require.True(t, paths[http.MethodGet+" /api/v1/desktop/checkout-sessions/:session_id"])
	require.True(t, paths[http.MethodPost+" /api/v1/desktop/checkout-sessions/:session_id/activate"])
}
