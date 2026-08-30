package handler

import (
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DesktopCheckoutHandler exposes a deliberately small payment surface for
// the first-party desktop helper. The actual payment provider interaction is
// still performed by PaymentService; this handler only adds an opaque,
// user/device-bound polling capability around it.
type DesktopCheckoutHandler struct {
	sessions *service.CheckoutSessionService
}

func NewDesktopCheckoutHandler(sessions *service.CheckoutSessionService) *DesktopCheckoutHandler {
	return &DesktopCheckoutHandler{sessions: sessions}
}

type desktopCheckoutCreateRequest struct {
	Amount                    float64 `json:"amount"`
	PaymentType               string  `json:"payment_type" binding:"required"`
	OrderType                 string  `json:"order_type"`
	PlanID                    int64   `json:"plan_id"`
	UpgradeFromSubscriptionID int64   `json:"upgrade_from_subscription_id"`
	PaymentSource             string  `json:"payment_source"`
}

// Create starts a hosted checkout and returns an opaque session id plus the
// provider's browser/QR details. The session expires after a short fixed TTL.
func (h *DesktopCheckoutHandler) Create(c *gin.Context) {
	if h == nil || h.sessions == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	var req desktopCheckoutCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid checkout request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.PaymentType) == "" {
		response.BadRequest(c, "payment_type is required")
		return
	}
	deviceID, _ := middleware2.GetDeviceIDFromContext(c)
	result, err := h.sessions.Create(c.Request.Context(), service.CreateCheckoutSessionInput{
		UserID:   subject.UserID,
		DeviceID: deviceID,
		Request: service.CreateOrderRequest{
			UserID:                    subject.UserID,
			Amount:                    req.Amount,
			PaymentType:               req.PaymentType,
			OrderType:                 req.OrderType,
			PlanID:                    req.PlanID,
			UpgradeFromSubscriptionID: req.UpgradeFromSubscriptionID,
			PaymentSource:             desktopCheckoutPaymentSource(req.PaymentSource),
			ClientIP:                  c.ClientIP(),
			Locale:                    c.GetHeader("Accept-Language"),
		},
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, result)
}

// Get polls a previously created checkout session. User/device binding is
// checked before any order status is queried, so a guessed id cannot become
// an order existence oracle.
func (h *DesktopCheckoutHandler) Get(c *gin.Context) {
	if h == nil || h.sessions == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	deviceID, _ := middleware2.GetDeviceIDFromContext(c)
	result, err := h.sessions.Get(c.Request.Context(), subject.UserID, deviceID, c.Param("session_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Activate turns a short-lived desktop checkout reservation into a regular
// payment order. It is intentionally browser-only: provider client secrets,
// redirect URLs and QR payloads are returned to the interactive web payment
// page, never to a desktop polling token.
func (h *DesktopCheckoutHandler) Activate(c *gin.Context) {
	if h == nil || h.sessions == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	result, err := h.sessions.ActivateWithBrowserContext(c.Request.Context(), subject.UserID, c.Param("session_id"), checkoutBrowserContext(c))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func checkoutBrowserContext(c *gin.Context) service.CheckoutBrowserContext {
	if c == nil || c.Request == nil {
		return service.CheckoutBrowserContext{}
	}
	context := service.CheckoutBrowserContext{
		ClientIP: c.ClientIP(),
		Locale:   c.GetHeader("Accept-Language"),
		Host:     c.Request.Host,
		Scheme:   checkoutRequestScheme(c),
	}
	// Production routes install the fixed desktop transport policy. Use its
	// canonical origin instead of reflecting request Host/X-Forwarded-Host into
	// a payment provider return URL.
	if origin := middleware2.DesktopPublicOriginFromContext(c); origin != "" {
		if parsed, err := url.Parse(origin); err == nil {
			context.Host, context.Scheme = parsed.Host, parsed.Scheme
		}
	}
	return context
}

func checkoutRequestScheme(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if c.Request.TLS != nil {
		return "https"
	}
	// The routed production handler has already validated forwarded headers via
	// RequireHTTPS. A direct invocation must not trust a caller-controlled XFP.
	return ""
}

func desktopCheckoutPaymentSource(value string) string {
	// The source is an audit/routing label, not a caller-controlled free-form
	// field. Keep it fixed so a desktop client cannot inject sensitive data or
	// control provider-specific payment routing through logs.
	return "desktop"
}
