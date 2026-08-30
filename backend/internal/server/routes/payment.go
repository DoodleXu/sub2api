package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterPaymentRoutes registers all payment-related routes:
// user-facing endpoints, webhook endpoints, and admin endpoints.
func RegisterPaymentRoutes(
	v1 *gin.RouterGroup,
	paymentHandler *handler.PaymentHandler,
	webhookHandler *handler.PaymentWebhookHandler,
	adminPaymentHandler *admin.PaymentHandler,
	jwtAuth middleware.JWTAuthMiddleware,
	adminAuth middleware.AdminAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	stepUpAuth middleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	// --- User-facing payment endpoints (authenticated) ---
	authenticated := v1.Group("/payment")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(middleware.BackendModeUserGuard(settingService))
	// The legacy payment API returns provider/order material and contains
	// mutations such as cancellation and refund requests. Keep it browser-only;
	// desktop sessions use the narrow /desktop/checkout-sessions bridge below,
	// where the browser performs the provider interaction and the device only
	// receives an opaque status.
	authenticated.Use(middleware.RequireBrowserSession())
	// 面板全局按用户限流
	authenticated.Use(panelRateLimiter.Global())
	{
		authenticated.GET("/config", paymentHandler.GetPaymentConfig)
		authenticated.GET("/checkout-info", paymentHandler.GetCheckoutInfo)
		authenticated.GET("/plans", paymentHandler.GetPlans)
		authenticated.GET("/limits", paymentHandler.GetLimits)
		authenticated.GET("/subscription-upgrade-options", paymentHandler.GetSubscriptionUpgradeOptions)

		orders := authenticated.Group("/orders")
		{
			orders.POST("", paymentHandler.CreateOrder)
			orders.POST("/verify", paymentHandler.VerifyOrder)
			orders.GET("/my", paymentHandler.GetMyOrders)
			orders.GET("/:id", paymentHandler.GetOrder)
			orders.POST("/:id/cancel", paymentHandler.CancelOrder)
			orders.POST("/:id/refund-request", paymentHandler.RequestRefund)
			orders.GET("/refund-eligible-providers", paymentHandler.GetRefundEligibleProviders)
		}
	}

	// --- Public payment endpoints (no auth) ---
	// Signed resume-token recovery proves possession of the checkout session.
	public := v1.Group("/payment/public")
	public.Use(panelRateLimiter.PublicIP())
	{
		public.POST("/orders/resolve", paymentHandler.ResolveOrderPublicByResumeToken)
	}

	// --- Webhook endpoints (no auth) ---
	webhook := v1.Group("/payment/webhook")
	{
		// EasyPay sends GET callbacks with query params
		webhook.GET("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/alipay", webhookHandler.AlipayNotify)
		webhook.POST("/wxpay", webhookHandler.WxpayNotify)
		webhook.POST("/stripe", webhookHandler.StripeWebhook)
		webhook.POST("/airwallex", webhookHandler.AirwallexWebhook)
	}

	// --- Admin payment endpoints (admin auth) ---
	adminGroup := v1.Group("/admin/payment")
	adminGroup.Use(gin.HandlerFunc(adminAuth))
	adminGroup.Use(gin.HandlerFunc(auditLog))
	adminGroup.Use(middleware.AdminComplianceGuard(settingService))
	{
		// Dashboard
		adminGroup.GET("/dashboard", adminPaymentHandler.GetDashboard)

		// Config
		adminGroup.GET("/config", adminPaymentHandler.GetConfig)
		adminGroup.PUT("/config", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.UpdateConfig)

		// Orders
		adminOrders := adminGroup.Group("/orders")
		{
			adminOrders.GET("", adminPaymentHandler.ListOrders)
			adminOrders.GET("/:id", adminPaymentHandler.GetOrderDetail)
			adminOrders.POST("/:id/cancel", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.CancelOrder)
			adminOrders.POST("/:id/retry", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.RetryFulfillment)
			adminOrders.GET("/:id/refund-preview", adminPaymentHandler.PreviewRefund)
			adminOrders.POST("/:id/refund", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.ProcessRefund)
			adminOrders.POST("/:id/refund/query", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.QueryAndFinalizeRefund)
		}

		// Subscription Plans
		plans := adminGroup.Group("/plans")
		{
			plans.GET("", adminPaymentHandler.ListPlans)
			plans.POST("", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.CreatePlan)
			plans.PUT("/:id", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.UpdatePlan)
			plans.DELETE("/:id", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.DeletePlan)
		}

		// Provider Instances
		providers := adminGroup.Group("/providers")
		{
			providers.GET("", adminPaymentHandler.ListProviders)
			providers.POST("", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.CreateProvider)
			providers.PUT("/:id", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.UpdateProvider)
			providers.DELETE("/:id", gin.HandlerFunc(stepUpAuth), adminPaymentHandler.DeleteProvider)
		}
	}
}
