package service

// Checkout sessions are the narrow bridge used by first-party desktop
// clients when a payment must be completed in a browser.  The session id is
// an opaque, short-lived Redis capability; it is never an order identifier and
// is always bound to the authenticated user (and, when present, device id).
// Payment credentials/provider secrets are deliberately not persisted in the
// session record.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/redis/go-redis/v9"
)

const (
	checkoutSessionKeyPrefix         = "desktop:checkout:session:"
	checkoutActivationKeySuffix      = ":activation"
	checkoutSessionTTL               = 15 * time.Minute
	checkoutActivationLockTTL        = 90 * time.Second
	checkoutSessionTokenBytes        = 32
	checkoutPollInterval             = 5
	checkoutSessionVersion           = 1
	checkoutDesktopPublicOrigin      = "https://ai.clol.site"
	checkoutSessionStatusActivating  = "activating"
	checkoutActivationReleaseTimeout = 2 * time.Second
)

var (
	ErrCheckoutSessionNotFound   = infraerrors.NotFound("CHECKOUT_SESSION_NOT_FOUND", "checkout session not found or expired")
	ErrCheckoutSessionForbidden  = infraerrors.Forbidden("CHECKOUT_SESSION_FORBIDDEN", "checkout session belongs to another session")
	ErrCheckoutSessionActivated  = infraerrors.Conflict("CHECKOUT_SESSION_ACTIVATED", "checkout session has already been activated")
	ErrCheckoutSessionActivating = infraerrors.Conflict("CHECKOUT_SESSION_ACTIVATING", "checkout session activation is already in progress")
)

// CheckoutOrderService is the small payment surface required by the session
// layer.  Keeping this interface separate makes the opaque-session mechanics
// independently testable and avoids coupling handlers to Ent entities.
type CheckoutOrderService interface {
	CreateOrder(context.Context, CreateOrderRequest) (*CreateOrderResponse, error)
	GetOrder(context.Context, int64, int64) (*dbent.PaymentOrder, error)
}

// CheckoutSessionService persists short-lived checkout capabilities and
// refreshes their status from the canonical payment-order service.
type CheckoutSessionService struct {
	redis  *redis.Client
	orders CheckoutOrderService
	now    func() time.Time
	// publicOrigin is a server-controlled HTTPS origin used for payment
	// provider callbacks. It is never taken from a desktop payload or request
	// Host header.
	publicOrigin string
}

func NewCheckoutSessionService(redisClient *redis.Client, orders CheckoutOrderService) *CheckoutSessionService {
	return &CheckoutSessionService{redis: redisClient, orders: orders, now: time.Now, publicOrigin: checkoutDesktopPublicOrigin}
}

// SetPublicOrigin allows a deployment with a different first-party hostname
// to configure the hosted checkout callback. Only an absolute HTTPS origin
// without userinfo, query, fragment, or path is accepted; invalid values leave
// the safe default in place.
func (s *CheckoutSessionService) SetPublicOrigin(origin string) {
	if s == nil {
		return
	}
	normalized := normalizeCheckoutOrigin(origin)
	if normalized != "" {
		s.publicOrigin = normalized
	}
}

type CreateCheckoutSessionInput struct {
	UserID   int64
	DeviceID string
	Request  CreateOrderRequest
}

// CheckoutBrowserContext is collected from the interactive browser request at
// activation time. It is deliberately not persisted in Redis and contains no
// user-controlled return URL or provider credential.
type CheckoutBrowserContext struct {
	ClientIP string
	Locale   string
	Host     string
	Scheme   string
}

// CheckoutSession is safe to return to a desktop client.  It intentionally
// omits Stripe/Airwallex client secrets; those are returned only by the
// existing order-create response and should not be cached in a bearer-like
// session record.
type CheckoutSession struct {
	SessionID                 string    `json:"session_id"`
	Status                    string    `json:"status"`
	OrderID                   int64     `json:"order_id,omitempty"`
	PaymentType               string    `json:"payment_type,omitempty"`
	OrderType                 string    `json:"order_type,omitempty"`
	PlanID                    int64     `json:"plan_id,omitempty"`
	UpgradeFromSubscriptionID int64     `json:"upgrade_from_subscription_id,omitempty"`
	ResultType                string    `json:"result_type,omitempty"`
	Amount                    float64   `json:"amount,omitempty"`
	PayAmount                 float64   `json:"pay_amount,omitempty"`
	Currency                  string    `json:"currency,omitempty"`
	BrowserURL                string    `json:"browser_url,omitempty"`
	ExpiresAt                 time.Time `json:"expires_at"`
	CreatedAt                 time.Time `json:"created_at"`
	PollAfter                 int       `json:"poll_after_seconds"`
	StatusURL                 string    `json:"status_url,omitempty"`
	LastOrderUpdate           time.Time `json:"last_order_update,omitempty"`
}

type checkoutSessionRecord struct {
	Version         int                    `json:"version"`
	SessionID       string                 `json:"session_id"`
	UserID          int64                  `json:"user_id"`
	DeviceID        string                 `json:"device_id,omitempty"`
	OrderID         int64                  `json:"order_id,omitempty"`
	PaymentType     string                 `json:"payment_type,omitempty"`
	ResultType      string                 `json:"result_type,omitempty"`
	Amount          float64                `json:"amount,omitempty"`
	PayAmount       float64                `json:"pay_amount,omitempty"`
	Currency        string                 `json:"currency,omitempty"`
	BrowserURL      string                 `json:"browser_url,omitempty"`
	Status          string                 `json:"status"`
	CreatedAt       time.Time              `json:"created_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
	LastOrderUpdate time.Time              `json:"last_order_update,omitempty"`
	Request         checkoutSessionRequest `json:"request"`
}

// checkoutSessionRequest is the deliberately allow-listed part of a payment
// request that may cross from the desktop helper to the browser activation
// endpoint. It excludes return URLs, IP/UA data, OpenID and any provider
// credential material. The browser supplies its own request context when it
// activates the session.
type checkoutSessionRequest struct {
	Amount                    float64 `json:"amount"`
	PaymentType               string  `json:"payment_type"`
	OrderType                 string  `json:"order_type,omitempty"`
	PlanID                    int64   `json:"plan_id,omitempty"`
	UpgradeFromSubscriptionID int64   `json:"upgrade_from_subscription_id,omitempty"`
}

func (s *CheckoutSessionService) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func checkoutSessionKey(sessionID string) string { return checkoutSessionKeyPrefix + sessionID }

func checkoutActivationKey(sessionID string) string {
	return checkoutSessionKey(sessionID) + checkoutActivationKeySuffix
}

func newCheckoutSessionID() (string, error) {
	b := make([]byte, checkoutSessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate checkout session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *CheckoutSessionService) Create(ctx context.Context, input CreateCheckoutSessionInput) (*CheckoutSession, error) {
	if s == nil || s.redis == nil || s.orders == nil {
		return nil, ErrServiceUnavailable
	}
	if input.UserID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHENTICATED", "user authentication is required")
	}
	if math.IsNaN(input.Request.Amount) || math.IsInf(input.Request.Amount, 0) || input.Request.Amount <= 0 {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "amount must be a positive finite number")
	}
	input.Request.UserID = input.UserID
	// A desktop checkout is always a hosted/browser flow.  Do not let a
	// caller smuggle an arbitrary source host or return URL into the session.
	input.Request.SrcHost = ""
	input.Request.SrcURL = ""
	input.Request.ReturnURL = ""
	input.Request.IsMobile = false
	input.Request.IsWeChatBrowser = false

	sessionID, err := newCheckoutSessionID()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	now := s.clock()
	if now.IsZero() {
		now = time.Now()
	}
	expiresAt := now.Add(checkoutSessionTTL)
	record := checkoutSessionRecord{
		Version: checkoutSessionVersion, SessionID: sessionID, UserID: input.UserID,
		DeviceID: strings.TrimSpace(input.DeviceID), PaymentType: strings.TrimSpace(input.Request.PaymentType),
		Amount: input.Request.Amount, Status: OrderStatusPending, CreatedAt: now, ExpiresAt: expiresAt,
		Request: checkoutSessionRequest{
			Amount: input.Request.Amount, PaymentType: strings.TrimSpace(input.Request.PaymentType),
			OrderType: strings.TrimSpace(input.Request.OrderType), PlanID: input.Request.PlanID,
			UpgradeFromSubscriptionID: input.Request.UpgradeFromSubscriptionID,
		},
	}
	record.BrowserURL = checkoutBrowserURL(sessionID)
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if err := s.redis.Set(ctx, checkoutSessionKey(sessionID), encoded, checkoutSessionTTL).Err(); err != nil {
		return nil, ErrServiceUnavailable
	}
	return checkoutSessionResponse(record), nil
}

func (s *CheckoutSessionService) Get(ctx context.Context, userID int64, deviceID, sessionID string) (*CheckoutSession, error) {
	if s == nil || s.redis == nil || s.orders == nil {
		return nil, ErrServiceUnavailable
	}
	if userID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHENTICATED", "user authentication is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 {
		return nil, ErrCheckoutSessionNotFound
	}
	record, err := s.loadRecord(ctx, userID, deviceID, sessionID)
	if err != nil {
		return nil, err
	}
	if record.OrderID > 0 {
		order, orderErr := s.orders.GetOrder(ctx, record.OrderID, userID)
		if orderErr != nil {
			// Preserve the opaque capability boundary: do not turn a DB error into
			// an order existence oracle for a guessed session id.
			return nil, ErrServiceUnavailable
		}
		if order != nil {
			record.Status = checkoutOrderStatus(order.Status)
			record.LastOrderUpdate = order.UpdatedAt
			if strings.TrimSpace(record.PaymentType) == "" {
				record.PaymentType = strings.TrimSpace(order.PaymentType)
			}
			_ = s.persistRecord(ctx, record)
		}
	}
	return checkoutSessionResponse(record), nil
}

// Activate creates the provider order only after an interactive browser
// session has opened the opaque URL. Provider details are returned directly to
// that browser request and are never written to the desktop session record or
// exposed through the desktop polling endpoint.
func (s *CheckoutSessionService) Activate(ctx context.Context, userID int64, sessionID string) (*CreateOrderResponse, error) {
	return s.ActivateWithBrowserContext(ctx, userID, sessionID, CheckoutBrowserContext{})
}

// ActivateWithBrowserContext is the browser-only half of the desktop hosted
// checkout flow. The context is ephemeral: it is used to construct a safe
// same-origin payment result URL and to preserve request-local accounting
// metadata, but is never written to the desktop session record.
func (s *CheckoutSessionService) ActivateWithBrowserContext(ctx context.Context, userID int64, sessionID string, browser CheckoutBrowserContext) (*CreateOrderResponse, error) {
	if s == nil || s.redis == nil || s.orders == nil {
		return nil, ErrServiceUnavailable
	}
	if userID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHENTICATED", "user authentication is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 {
		return nil, ErrCheckoutSessionNotFound
	}
	lockValue, err := newCheckoutSessionID()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	lockKey := checkoutActivationKey(sessionID)
	locked, err := s.redis.SetNX(ctx, lockKey, lockValue, checkoutActivationLockTTL).Result()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if !locked {
		return nil, ErrCheckoutSessionActivating
	}
	// Never unconditionally DEL a distributed lock: if the lock expires and is
	// acquired by another worker, an old activation must not delete the newer
	// worker's lock. Use a compare-and-delete script on a context that survives
	// request cancellation for a short bounded release window.
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkoutActivationReleaseTimeout)
		defer cancel()
		_ = releaseCheckoutActivationLock(releaseCtx, s.redis, lockKey, lockValue)
	}()

	// The lock is deliberately acquired before reading the record. This avoids
	// a stale pending snapshot racing with a previous activation that has just
	// committed its order id.
	record, err := s.loadRecord(ctx, userID, "", sessionID)
	if err != nil {
		return nil, err
	}
	if record.OrderID > 0 {
		return nil, ErrCheckoutSessionActivated
	}
	if strings.EqualFold(strings.TrimSpace(record.Status), checkoutSessionStatusActivating) {
		// A durable activating marker protects the payment order even when the
		// short lock TTL has elapsed (for example while a provider call is slow or
		// the worker crashes after creating the order).
		return nil, ErrCheckoutSessionActivating
	}

	// Mark the session before invoking the provider. If the provider succeeds
	// but the final order checkpoint cannot be written, retries still observe
	// this marker and are refused instead of creating a second order.
	record.Status = checkoutSessionStatusActivating
	if err := s.persistRecord(ctx, record); err != nil {
		if errors.Is(err, ErrCheckoutSessionNotFound) {
			return nil, ErrCheckoutSessionNotFound
		}
		return nil, ErrServiceUnavailable
	}

	request := CreateOrderRequest{
		UserID: userID, Amount: record.Request.Amount, PaymentType: record.Request.PaymentType,
		OrderType: record.Request.OrderType, PlanID: record.Request.PlanID,
		UpgradeFromSubscriptionID: record.Request.UpgradeFromSubscriptionID,
		PaymentSource:             PaymentSourceHostedRedirect,
		IsMobile:                  false, IsWeChatBrowser: false,
	}
	s.applyCheckoutBrowserContext(&request, browser)
	// Legacy records created before the request allow-list was introduced can
	// still be activated from their safe top-level fields.
	if strings.TrimSpace(request.PaymentType) == "" {
		request.PaymentType = strings.TrimSpace(record.PaymentType)
	}
	if request.Amount == 0 {
		request.Amount = record.Amount
	}
	order, err := s.orders.CreateOrder(ctx, request)
	if err != nil {
		// Keep the activating marker on provider errors. The payment service may
		// have created a local order before a network/provider failure is returned;
		// allowing an immediate retry would make duplicate charges possible.
		return nil, err
	}
	if order == nil || order.OrderID <= 0 {
		return nil, ErrServiceUnavailable
	}
	record.OrderID = order.OrderID
	record.PaymentType = strings.TrimSpace(order.PaymentType)
	record.ResultType = strings.TrimSpace(string(order.ResultType))
	record.Amount = order.Amount
	record.PayAmount = order.PayAmount
	record.Currency = strings.TrimSpace(order.Currency)
	record.Status = checkoutOrderStatus(order.Status)
	if strings.EqualFold(strings.TrimSpace(string(order.ResultType)), "oauth_required") {
		record.Status = "action_required"
	}
	if !order.ExpiresAt.IsZero() && order.ExpiresAt.Before(record.ExpiresAt) {
		record.ExpiresAt = order.ExpiresAt
	}
	if err := s.persistRecord(ctx, record); err != nil {
		// The browser already holds the provider response. Returning it keeps the
		// payment flow usable even if the polling checkpoint fails. The durable
		// activating marker written above remains in Redis, so a retry cannot create
		// another order. A later status query will remain activating/expire rather
		// than exposing provider credentials from Redis.
		return order, nil
	}
	return order, nil
}

func (s *CheckoutSessionService) loadRecord(ctx context.Context, userID int64, deviceID, sessionID string) (checkoutSessionRecord, error) {
	var record checkoutSessionRecord
	if userID <= 0 {
		return record, infraerrors.Unauthorized("UNAUTHENTICATED", "user authentication is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 {
		return record, ErrCheckoutSessionNotFound
	}
	raw, err := s.redis.Get(ctx, checkoutSessionKey(sessionID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return record, ErrCheckoutSessionNotFound
		}
		return record, ErrServiceUnavailable
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.Version != checkoutSessionVersion || record.UserID != userID {
		return record, ErrCheckoutSessionNotFound
	}
	if record.DeviceID != "" && strings.TrimSpace(deviceID) != "" && strings.TrimSpace(deviceID) != record.DeviceID {
		return record, ErrCheckoutSessionForbidden
	}
	if !record.ExpiresAt.IsZero() && !s.clock().Before(record.ExpiresAt) {
		return record, ErrCheckoutSessionNotFound
	}
	return record, nil
}

func (s *CheckoutSessionService) persistRecord(ctx context.Context, record checkoutSessionRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	result, err := persistCheckoutSessionScript.Run(ctx, s.redis, []string{checkoutSessionKey(record.SessionID)}, encoded).Int64()
	if err != nil {
		return err
	}
	switch result {
	case -2, 0:
		// -2 means the key disappeared; zero means it is expiring during this
		// atomic operation. Both are equivalent to an expired capability.
		return ErrCheckoutSessionNotFound
	case -1:
		// A checkout session must always have an expiry. Refuse to rewrite a
		// persistent key because doing so would turn a short-lived capability into
		// an unbounded one.
		return ErrServiceUnavailable
	default:
		return nil
	}
}

// releaseCheckoutActivationLock deletes key only when it still contains the
// value owned by this activation attempt. This is the standard Redis
// compare-and-delete pattern for single-flight locks.
func releaseCheckoutActivationLock(ctx context.Context, client *redis.Client, key, value string) error {
	if client == nil || strings.TrimSpace(key) == "" || value == "" {
		return nil
	}
	_, err := checkoutActivationReleaseScript.Run(ctx, client, []string{key}, value).Result()
	return err
}

var checkoutActivationReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

// persistCheckoutSessionScript performs the existence/TTL check and the
// KEEPTTL write in one Redis execution. A GET+TTL followed by SET would allow a
// key to expire between commands and could recreate it without an expiry.
var persistCheckoutSessionScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
    return -2
end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then
    return ttl
end
redis.call("SET", KEYS[1], ARGV[1], "KEEPTTL")
return ttl
`)

func checkoutSessionResponse(record checkoutSessionRecord) *CheckoutSession {
	statusURL := "/api/v1/desktop/checkout-sessions/" + url.PathEscape(record.SessionID)
	return &CheckoutSession{
		SessionID: record.SessionID, Status: record.Status, OrderID: record.OrderID,
		PaymentType: record.PaymentType, OrderType: record.Request.OrderType, PlanID: record.Request.PlanID,
		UpgradeFromSubscriptionID: record.Request.UpgradeFromSubscriptionID,
		ResultType:                record.ResultType, Amount: record.Amount,
		PayAmount: record.PayAmount, Currency: record.Currency, BrowserURL: record.BrowserURL,
		ExpiresAt: record.ExpiresAt,
		CreatedAt: record.CreatedAt, PollAfter: checkoutPollInterval, StatusURL: statusURL,
		LastOrderUpdate: record.LastOrderUpdate,
	}
}

func (s *CheckoutSessionService) applyCheckoutBrowserContext(request *CreateOrderRequest, browser CheckoutBrowserContext) {
	if request == nil {
		return
	}
	request.ClientIP = limitCheckoutText(browser.ClientIP, 128)
	request.Locale = limitCheckoutText(browser.Locale, 128)
	origin := checkoutDesktopPublicOrigin
	if s != nil && normalizeCheckoutOrigin(s.publicOrigin) != "" {
		origin = normalizeCheckoutOrigin(s.publicOrigin)
	}
	parsed, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return
	}
	request.SrcHost = parsed.Host
	request.ReturnURL = strings.TrimRight(origin, "/") + paymentResultReturnPath
}

func normalizeCheckoutOrigin(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	u.Path = ""
	return strings.TrimRight(u.String(), "/")
}

func limitCheckoutText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}

func normalizeCheckoutHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\r\n/@%?#\\") {
		return ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] <= 0x20 || value[i] == 0x7f || value[i] >= 0x80 {
			return ""
		}
	}
	parsed, err := url.Parse("https://" + value + "/")
	if err != nil || parsed.User != nil || parsed.Host != value || parsed.Path != "/" {
		return ""
	}
	if host, _, splitErr := net.SplitHostPort(value); splitErr == nil && host == "" {
		return ""
	}
	return value
}

func checkoutOrderStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return OrderStatusPending
	}
	return status
}

func checkoutBrowserURL(sessionID string) string {
	// Always return a same-origin relative path. Provider pay URLs, QR payloads,
	// Stripe client secrets and payment resume tokens are intentionally omitted
	// from this response and from Redis. The hosted page can use the opaque id
	// to resume the flow after the user signs in.
	return "/purchase?checkout_session_id=" + url.QueryEscape(sessionID)
}
