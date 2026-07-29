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

func TestPaymentAdminMutationsRequireStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	stepUpCalls := 0
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusTeapot)
	})

	RegisterPaymentRoutes(
		router.Group("/api/v1"),
		&handler.PaymentHandler{},
		&handler.PaymentWebhookHandler{},
		adminhandler.NewPaymentHandler(nil, nil),
		servermiddleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		stepUp,
		nil,
		nil,
	)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/v1/admin/payment/config"},
		{http.MethodPost, "/api/v1/admin/payment/orders/1/cancel"},
		{http.MethodPost, "/api/v1/admin/payment/orders/1/retry"},
		{http.MethodPost, "/api/v1/admin/payment/orders/1/refund"},
		{http.MethodPost, "/api/v1/admin/payment/orders/1/refund/query"},
		{http.MethodPost, "/api/v1/admin/payment/plans"},
		{http.MethodPut, "/api/v1/admin/payment/plans/1"},
		{http.MethodDelete, "/api/v1/admin/payment/plans/1"},
		{http.MethodPost, "/api/v1/admin/payment/providers"},
		{http.MethodPut, "/api/v1/admin/payment/providers/1"},
		{http.MethodDelete, "/api/v1/admin/payment/providers/1"},
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
