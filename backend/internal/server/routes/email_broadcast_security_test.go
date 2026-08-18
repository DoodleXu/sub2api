//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmailBroadcastSensitiveRoutesRequireStepUp(t *testing.T) {
	router := gin.New()
	stepUpCalls := 0
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusTeapot)
	})
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Setting: adminhandler.NewSettingHandler(nil, nil, nil, nil, nil, nil, nil)}}
	registerSettingsRoutes(router.Group("/api/v1/admin"), handlers, stepUp)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/admin/settings/email-broadcasts"},
		{http.MethodPost, "/api/v1/admin/settings/email-broadcasts/preflight"},
		{http.MethodPost, "/api/v1/admin/settings/email-broadcasts/batch-1/cancel"},
		{http.MethodPost, "/api/v1/admin/settings/email-broadcasts/batch-1/resume"},
		{http.MethodGet, "/api/v1/admin/settings/email-broadcasts/batch-1/recipients"},
		{http.MethodGet, "/api/v1/admin/settings/email-broadcasts/draft"},
		{http.MethodPut, "/api/v1/admin/settings/email-broadcasts/draft"},
		{http.MethodDelete, "/api/v1/admin/settings/email-broadcasts/draft"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			before := stepUpCalls
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, tc.path, nil)
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusTeapot, recorder.Code)
			require.Equal(t, before+1, stepUpCalls)
		})
	}
}
