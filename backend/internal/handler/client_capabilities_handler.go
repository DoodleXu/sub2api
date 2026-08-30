package handler

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ClientCapabilitiesHandler returns a stable, public bootstrap document. A
// desktop client can probe a site before it stores credentials and avoid
// guessing which fork-specific endpoints are available.
type ClientCapabilitiesHandler struct {
	settings            *service.SettingService
	apiKeys             *service.APIKeyService
	version             string
	imageTaskResolver   func(context.Context) (enabled, pollable bool)
	backendModeResolver func(context.Context) bool
}

// SetBackendModeResolver keeps the capability document on the same cached
// control-plane decision as the route guards and gateway capability endpoint.
// Without this injection a settings read and a cached guard decision could
// briefly disagree during a backend-mode toggle.
func (h *ClientCapabilitiesHandler) SetBackendModeResolver(resolver func(context.Context) bool) {
	if h != nil {
		h.backendModeResolver = resolver
	}
}

func NewClientCapabilitiesHandler(settings *service.SettingService, buildInfo BuildInfo, apiKeys ...*service.APIKeyService) *ClientCapabilitiesHandler {
	var apiKeyService *service.APIKeyService
	if len(apiKeys) > 0 {
		apiKeyService = apiKeys[0]
	}
	return &ClientCapabilitiesHandler{settings: settings, apiKeys: apiKeyService, version: buildInfo.Version}
}

// SetImageTaskCapabilityResolver wires the runtime async-image admission
// state. It is kept as a setter so the Wire constructor remains backwards
// compatible with embedders and focused handler tests that do not build the
// image task service.
func (h *ClientCapabilitiesHandler) SetImageTaskCapabilityResolver(resolver func(context.Context) (enabled, pollable bool)) {
	if h != nil {
		h.imageTaskResolver = resolver
	}
}

type clientCapabilities struct {
	ProtocolVersion string                     `json:"protocol_version"`
	ServerVersion   string                     `json:"server_version,omitempty"`
	ClientID        string                     `json:"client_id"`
	Audience        string                     `json:"audience"`
	APIBaseURL      string                     `json:"api_base_url,omitempty"`
	Scopes          []string                   `json:"scopes"`
	DefaultScopes   []string                   `json:"default_scopes"`
	HighRiskScopes  []string                   `json:"high_risk_scopes"`
	Features        map[string]bool            `json:"features"`
	Availability    map[string]string          `json:"availability"`
	BackendMode     bool                       `json:"backend_mode_enabled"`
	AsyncImages     clientAsyncImageCapability `json:"async_images"`
	Endpoints       map[string]string          `json:"endpoints"`
	DeviceFlow      clientDeviceFlowCapability `json:"device_flow"`
}

type clientAsyncImageCapability struct {
	Enabled  bool   `json:"enabled"`
	Pollable bool   `json:"pollable"`
	Reason   string `json:"reason,omitempty"`
}

type clientDeviceFlowCapability struct {
	GrantType        string   `json:"grant_type"`
	ExpiresInSeconds int      `json:"expires_in_seconds"`
	PollInterval     int      `json:"poll_interval_seconds"`
	PKCEMethods      []string `json:"pkce_methods"`
	PublicKeyBinding string   `json:"public_key_binding"`
	TokenType        string   `json:"token_type"`
	DPoPAlgorithms   []string `json:"dpop_algorithms"`
	PublicKeyCurves  []string `json:"public_key_curves"`
	ProofHeader      string   `json:"proof_header"`
	NonceRequired    bool     `json:"nonce_required"`
	AccessTokenHash  string   `json:"access_token_hash"`
}

func (h *ClientCapabilitiesHandler) Get(c *gin.Context) {
	// The desktop binary is pinned to the first-party origin.  A deployment may
	// still expose a path (for example /v1) through public settings, but an
	// untrusted host must never be able to redirect a fresh client before it has
	// completed device authorization.
	apiBaseURL := middleware2.DefaultDesktopPublicOrigin
	paymentEnabled := false
	webConsoleEnabled := false
	backendModeEnabled := false
	if h != nil && h.settings != nil {
		settings, err := h.settings.GetPublicSettings(c.Request.Context())
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if settings != nil {
			apiBaseURL = trustedDesktopAPIBaseURL(settings.APIBaseURL)
			paymentEnabled = settings.PaymentEnabled
			webConsoleEnabled = settings.WebConsoleEnabled
			backendModeEnabled = settings.BackendModeEnabled
		}
	}
	if h != nil && h.backendModeResolver != nil {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		backendModeEnabled = h.backendModeResolver(ctx)
	}
	// A handler without an injected resolver is used by compatibility embedders;
	// retain the historical feature flags there. Production router setup always
	// injects the resolver and therefore reports the actual storage gate.
	asyncEnabled, asyncPollable := true, true
	resolverKnown := false
	if h != nil && h.imageTaskResolver != nil {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		asyncEnabled, asyncPollable = h.imageTaskResolver(ctx)
		resolverKnown = true
	}
	features := map[string]bool{
		"desktop_device_authorization":  true,
		"pkce_s256":                     true,
		"public_key_thumbprint_binding": true,
		"refresh_token_rotation":        true,
		"device_session_revocation":     true,
		"balance":                       true,
		"usage":                         true,
		"billing":                       paymentEnabled,
		"checkout_sessions":             paymentEnabled,
		"checkin":                       true,
		"images":                        true,
		"image_tasks":                   true,
		"async_images":                  asyncEnabled,
		"web_console":                   webConsoleEnabled,
		"api_keys":                      true,
	}
	availability := make(map[string]string, len(features))
	for feature, enabled := range features {
		if enabled {
			availability[feature] = "available"
		} else {
			availability[feature] = "disabled"
		}
	}
	if resolverKnown && !asyncPollable {
		availability["async_images"] = "task_store_unavailable"
	} else if resolverKnown && !asyncEnabled {
		availability["async_images"] = "object_storage_unavailable"
	}
	if resolverKnown && !asyncPollable {
		features["image_tasks"] = false
		availability["image_tasks"] = "task_store_unavailable"
	}
	if backendModeEnabled {
		// Backend mode intentionally blocks non-admin self-service routes. API-key
		// gateway images remain available, but a fresh desktop enrollment and the
		// account/checkout surfaces cannot be used by ordinary users.
		for _, feature := range []string{
			"desktop_device_authorization", "balance", "usage", "billing", "checkout_sessions",
			"checkin", "image_tasks", "async_images", "api_keys", "device_session_revocation", "web_console",
		} {
			features[feature] = false
			availability[feature] = "backend_mode_disabled"
		}
	}
	asyncReason := ""
	if resolverKnown {
		switch {
		case !asyncPollable:
			asyncReason = "task_store_unavailable"
		case !asyncEnabled:
			asyncReason = "object_storage_unavailable"
		}
	}
	if backendModeEnabled {
		// Keep the detailed async block consistent with the feature map. The
		// gateway may still expose synchronous API-key image routes in backend
		// mode, but desktop task admission is a user-facing self-service route and
		// must not be advertised as usable while the mode guard is active.
		asyncEnabled = false
		asyncReason = "backend_mode_disabled"
	}
	version := ""
	if h != nil {
		version = h.version
	}
	response.Success(c, clientCapabilities{
		ProtocolVersion: "1",
		ServerVersion:   version,
		ClientID:        service.DesktopClientID,
		Audience:        service.DesktopAudience,
		APIBaseURL:      apiBaseURL,
		Scopes: []string{
			"openid", "profile", "balance", "usage", "billing", "checkin", "images", "api_keys",
		},
		DefaultScopes:  []string{"openid", "profile", "balance", "usage"},
		HighRiskScopes: []string{"billing", "api_keys"},
		Features:       features,
		Availability:   availability,
		BackendMode:    backendModeEnabled,
		AsyncImages:    clientAsyncImageCapability{Enabled: asyncEnabled, Pollable: asyncPollable, Reason: asyncReason},
		Endpoints: map[string]string{
			"public_settings":     "/api/v1/settings/public",
			"authorize_device":    "/api/v1/desktop/device-authorizations",
			"exchange_device":     "/api/v1/desktop/token",
			"logout_device":       "/api/v1/desktop/logout",
			"checkout_sessions":   "/api/v1/desktop/checkout-sessions",
			"checkout_activation": "/api/v1/desktop/checkout-sessions/{id}/activate (browser)",
			"auth_alias":          "/api/v1/auth/device/*",
			"approve_device":      "/api/v1/user/device/approve",
			"device_approval":     "/api/v1/user/device/approval",
			"devices":             "/api/v1/user/devices",
			"profile":             "/api/v1/user/profile",
			"balance":             "/api/v1/user/balance",
			"usage":               "/api/v1/usage",
			"checkin":             "/api/v1/user/checkin",
			"api_keys":            "/api/v1/keys",
			// Provider/order details and balance mutations stay in the browser
			// payment surface. Desktop clients use the opaque checkout session above
			// and must not infer that these legacy endpoints are in their grant.
			"payment":           "/api/v1/payment/checkout-info (browser)",
			"orders":            "/api/v1/payment/orders (browser)",
			"redeem":            "/api/v1/redeem (browser)",
			"image_tasks":       "/api/v1/user/image-tasks",
			"images":            "/v1/images/generations",
			"image_edits":       "/v1/images/edits",
			"async_images":      "/v1/images/generations/async",
			"async_image_edits": "/v1/images/edits/async",
		},
		DeviceFlow: clientDeviceFlowCapability{
			GrantType:        service.DesktopGrantType,
			ExpiresInSeconds: service.DesktopAuthorizationExpiresInSeconds,
			PollInterval:     5,
			PKCEMethods:      []string{"S256"},
			PublicKeyBinding: "jwk_thumbprint_sha256",
			TokenType:        "DPoP",
			DPoPAlgorithms:   []string{"ES256"},
			PublicKeyCurves:  []string{"P-256"},
			ProofHeader:      "DPoP",
			NonceRequired:    true,
			AccessTokenHash:  "sha-256-base64url",
		},
	})
}

func trustedDesktopAPIBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return middleware2.DefaultDesktopPublicOrigin
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		!strings.EqualFold(parsed.Host, "ai.clol.site") {
		return middleware2.DefaultDesktopPublicOrigin
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String()
}

// IntegrationProfiles is a small machine-readable companion to capabilities.
// It describes how a client should configure the site without returning any
// credential material or provider secrets.
func (h *ClientCapabilitiesHandler) IntegrationProfiles(c *gin.Context) {
	// The no-query form is public and credential-free. A key-specific request is
	// authenticated by the route's conditional JWT middleware and resolved below
	// through APIKeyService ownership checks; this keeps the documented query
	// contract while avoiding API-key ID enumeration.
	if raw, present := c.GetQuery("api_key_id"); present {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "api_key_id must be a positive integer")
			return
		}
		subject, ok := middleware2.GetAuthSubjectFromContext(c)
		if !ok {
			response.Unauthorized(c, "User not authenticated")
			return
		}
		if h == nil || h.apiKeys == nil {
			response.ErrorFrom(c, service.ErrServiceUnavailable)
			return
		}
		key, keyErr := h.apiKeys.GetByID(c.Request.Context(), id)
		if keyErr != nil {
			response.ErrorFrom(c, keyErr)
			return
		}
		// Return 404 for an existing key owned by another account as well as a
		// missing key. This prevents an authenticated caller from enumerating
		// another user's key IDs or status.
		if key == nil || key.UserID != subject.UserID {
			response.NotFound(c, "API key not found")
			return
		}
		expiresAt := ""
		if key.ExpiresAt != nil {
			expiresAt = key.ExpiresAt.UTC().Format(time.RFC3339)
		}
		available := strings.EqualFold(strings.TrimSpace(key.Status), service.StatusAPIKeyActive) && (key.ExpiresAt == nil || key.ExpiresAt.After(time.Now().UTC()))
		response.Success(c, gin.H{
			"key_specific": true,
			"api_key": gin.H{
				"id": id, "name": key.Name, "status": key.Status,
				"expires_at": expiresAt, "available": available,
			},
			"profiles": keySpecificIntegrationProfiles(id, available),
		})
		return
	}
	profiles := []gin.H{
		{
			"id":                 "desktop",
			"client_id":          service.DesktopClientID,
			"audience":           service.DesktopAudience,
			"auth":               "device_code_pkce",
			"grant_type":         service.DesktopGrantType,
			"refresh_grant_type": "refresh_token",
			"base_path":          "/api/v1",
			"endpoints":          []string{"/desktop/device-authorizations", "/desktop/token", "/desktop/checkout-sessions"},
			"configuration":      []string{"api_base_url", "access_token", "refresh_token"},
		},
		{
			"id":            "openai-compatible",
			"auth":          "api_key",
			"base_path":     "/v1",
			"configuration": []string{"api_base_url", "api_key"},
		},
		{
			"id":               "openai-images",
			"auth":             "api_key",
			"base_path":        "/v1",
			"endpoints":        []string{"/images/generations", "/images/edits", "/images/generations/async", "/images/edits/async"},
			"async_capability": "use_client_capabilities.async_images before submitting async tasks",
			"configuration":    []string{"api_base_url", "api_key", "model", "size", "quality", "output_format"},
		},
	}
	response.Success(c, gin.H{"profiles": profiles})
}

func keySpecificIntegrationProfiles(apiKeyID int64, available bool) []gin.H {
	return []gin.H{
		{
			"id": "openai-compatible", "auth": "api_key", "base_path": "/v1",
			"api_key_id": apiKeyID, "available": available,
			"configuration": []string{"api_base_url", "api_key"},
		},
		{
			"id": "openai-images", "auth": "api_key", "base_path": "/v1",
			"api_key_id": apiKeyID, "available": available,
			"endpoints":     []string{"/images/generations", "/images/edits", "/images/generations/async", "/images/edits/async"},
			"configuration": []string{"api_base_url", "api_key", "model", "size", "quality", "output_format"},
		},
	}
}
