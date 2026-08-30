//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runDesktopScopeMiddleware(t *testing.T, middleware gin.HandlerFunc, setup func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if setup != nil {
		r.Use(func(c *gin.Context) {
			setup(c)
			c.Next()
		})
	}
	r.GET("/check", middleware, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/check", nil))
	return recorder
}

func TestRequireDesktopScopeBrowserJWTRemainsCompatible(t *testing.T) {
	recorder := runDesktopScopeMiddleware(t, RequireDesktopScope("usage"), nil)
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestRequireDesktopScopeRejectsMissingAndUnknownDeviceScope(t *testing.T) {
	for name, scopes := range map[string]any{
		"missing": nil,
		"wrong":   []string{"profile"},
		"invalid": "usage",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := runDesktopScopeMiddleware(t, RequireDesktopScope("usage"), func(c *gin.Context) {
				c.Set(string(ContextKeyDeviceID), "device-1")
				if scopes != nil {
					c.Set(string(ContextKeyScopes), scopes)
				}
			})
			require.Equal(t, http.StatusForbidden, recorder.Code)
		})
	}
}

func TestRequireDesktopScopeAllowsGrantedDeviceScope(t *testing.T) {
	recorder := runDesktopScopeMiddleware(t, RequireDesktopScope("usage"), func(c *gin.Context) {
		c.Set(string(ContextKeyDeviceID), "device-1")
		c.Set(string(ContextKeyScopes), []string{"profile", "usage"})
	})
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestRequireDesktopScopesRequiresEveryCompositeScope(t *testing.T) {
	recorder := runDesktopScopeMiddleware(t, RequireDesktopScopes("profile", "balance"), func(c *gin.Context) {
		c.Set(string(ContextKeyDeviceID), "device-1")
		c.Set(string(ContextKeyScopes), []string{"profile"})
	})
	require.Equal(t, http.StatusForbidden, recorder.Code)

	recorder = runDesktopScopeMiddleware(t, RequireDesktopScopes("profile", "balance"), func(c *gin.Context) {
		c.Set(string(ContextKeyDeviceID), "device-1")
		c.Set(string(ContextKeyScopes), []string{"profile", "balance"})
	})
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestRequireBrowserSessionRejectsDeviceToken(t *testing.T) {
	recorder := runDesktopScopeMiddleware(t, RequireBrowserSession(), func(c *gin.Context) {
		c.Set(string(ContextKeyDeviceID), "device-1")
	})
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestRequireOwnDesktopDeviceAllowsOnlyBoundDevice(t *testing.T) {
	for name, requested := range map[string]string{
		"own device":   "device-1",
		"other device": "device-2",
	} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := gin.New()
			r.DELETE("/devices/:device_id", func(c *gin.Context) {
				c.Set(string(ContextKeyDeviceID), "device-1")
				c.Set(string(ContextKeyScopes), []string{"profile"})
				c.Next()
			}, RequireDesktopScope("profile"), RequireOwnDesktopDevice("device_id"), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/devices/"+requested, nil)
			r.ServeHTTP(recorder, req)
			if requested == "device-1" {
				require.Equal(t, http.StatusNoContent, recorder.Code)
			} else {
				require.Equal(t, http.StatusForbidden, recorder.Code)
				require.Contains(t, recorder.Body.String(), `"code":"DESKTOP_DEVICE_SELF_ONLY"`)
			}
		})
	}
}

func TestRequireOwnDesktopDevicePreservesBrowserCrossDeviceRevoke(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.DELETE("/devices/:device_id", RequireOwnDesktopDevice("device_id"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/devices/other-device", nil))
	require.Equal(t, http.StatusNoContent, recorder.Code)
}
