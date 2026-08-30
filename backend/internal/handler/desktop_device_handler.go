package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DesktopDeviceHandler exposes the OAuth-device-style flow used by the
// cross-platform desktop helper.  It intentionally contains no desktop UI or
// OS-specific code.
type DesktopDeviceHandler struct {
	devices *service.DesktopDeviceService
}

func NewDesktopDeviceHandler(devices *service.DesktopDeviceService) *DesktopDeviceHandler {
	return &DesktopDeviceHandler{devices: devices}
}

type desktopAuthorizeRequest struct {
	ClientID            string          `json:"client_id"`
	DeviceName          string          `json:"device_name"`
	PublicKey           json.RawMessage `json:"public_key"`
	CodeChallenge       string          `json:"code_challenge"`
	CodeChallengeMethod string          `json:"code_challenge_method"`
	Scope               json.RawMessage `json:"scope"`
	Audience            string          `json:"audience"`
	ProtectionLevel     string          `json:"protection_level"`
}

type desktopApproveRequest struct {
	UserCode string   `json:"user_code"`
	Approved *bool    `json:"approved"`
	Scopes   []string `json:"scopes"`
}

type desktopTokenRequest struct {
	GrantType    string          `json:"grant_type"`
	DeviceCode   string          `json:"device_code"`
	RefreshToken string          `json:"refresh_token"`
	CodeVerifier string          `json:"code_verifier"`
	PublicKey    json.RawMessage `json:"public_key"`
	ClientID     string          `json:"client_id"`
	Audience     string          `json:"audience"`
}

type desktopLogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *DesktopDeviceHandler) Authorize(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	var req desktopAuthorizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, service.ErrDesktopInvalidRequest)
		return
	}
	scopes, err := parseDesktopScope(req.Scope)
	if err != nil {
		response.ErrorFrom(c, service.ErrDesktopInvalidRequest)
		return
	}
	result, err := h.devices.CreateAuthorization(c.Request.Context(), service.DesktopDeviceAuthorizationInput{
		ClientID:            req.ClientID,
		DeviceName:          req.DeviceName,
		PublicKey:           req.PublicKey,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		Scopes:              scopes,
		Audience:            req.Audience,
		ProtectionLevel:     req.ProtectionLevel,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DesktopDeviceHandler) Approve(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if _, isDevice := middleware2.GetDeviceIDFromContext(c); isDevice {
		response.ErrorFrom(c, service.ErrInsufficientPerms)
		return
	}
	c.Header("Cache-Control", "no-store")
	var req desktopApproveRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.UserCode) == "" || req.Approved == nil {
		response.ErrorFrom(c, service.ErrDesktopInvalidRequest)
		return
	}
	if err := h.devices.ApproveAuthorization(c.Request.Context(), subject.UserID, req.UserCode, *req.Approved, req.Scopes); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"approved": *req.Approved})
}

// ApprovalDetails lets the authenticated browser show exactly what the
// desktop requested before the user chooses a scope subset. No credential or
// device public key is returned.
func (h *DesktopDeviceHandler) ApprovalDetails(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	if _, ok := middleware2.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if _, isDevice := middleware2.GetDeviceIDFromContext(c); isDevice {
		response.ErrorFrom(c, service.ErrInsufficientPerms)
		return
	}
	c.Header("Cache-Control", "no-store")
	userCode := strings.TrimSpace(c.Query("user_code"))
	if userCode == "" {
		response.ErrorFrom(c, service.ErrDesktopInvalidRequest)
		return
	}
	result, err := h.devices.GetAuthorizationForApproval(c.Request.Context(), userCode)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Token follows RFC 8628's machine-readable error shape so desktop polling can
// distinguish authorization_pending from a terminal denial/expiry.
func (h *DesktopDeviceHandler) Token(c *gin.Context) {
	if h == nil || h.devices == nil {
		writeDesktopOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "desktop authorization service unavailable")
		return
	}
	var req desktopTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_request", "invalid desktop token request")
		return
	}
	grantType := strings.TrimSpace(req.GrantType)
	switch grantType {
	case "refresh_token":
		if strings.TrimSpace(req.RefreshToken) == "" || strings.TrimSpace(req.DeviceCode) != "" || strings.TrimSpace(req.CodeVerifier) != "" {
			writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_request", "refresh token requests must contain only refresh_token")
			return
		}
		result, err := h.devices.Refresh(c.Request.Context(), service.DesktopDeviceRefreshInput{
			ClientID:     req.ClientID,
			Audience:     req.Audience,
			RefreshToken: req.RefreshToken,
			PublicKey:    req.PublicKey,
			DPoPProof:    c.GetHeader("DPoP"),
			Method:       c.Request.Method,
			TargetURL:    desktopRequestURL(c),
		})
		if err != nil {
			writeDesktopTokenError(c, err)
			return
		}
		c.Header("DPoP-Nonce", result.DPoPNonce)
		c.JSON(http.StatusOK, result)
		return
	case service.DesktopGrantType:
		if strings.TrimSpace(req.DeviceCode) == "" || strings.TrimSpace(req.CodeVerifier) == "" || strings.TrimSpace(req.RefreshToken) != "" {
			writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_request", "device authorization requests require device_code and code_verifier")
			return
		}
	default:
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_request", "unsupported grant_type")
		return
	}
	result, err := h.devices.ExchangeAuthorization(c.Request.Context(), service.DesktopDeviceTokenInput{
		DeviceCode:   req.DeviceCode,
		CodeVerifier: req.CodeVerifier,
		PublicKey:    req.PublicKey,
		ClientID:     req.ClientID,
		Audience:     req.Audience,
	})
	if err != nil {
		writeDesktopTokenError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func desktopRequestURL(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return middleware2.RequestURLForDPoPFromContext(c)
}

func (h *DesktopDeviceHandler) Logout(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	var req desktopLogoutRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.RefreshToken) == "" {
		response.ErrorFrom(c, service.ErrRefreshTokenInvalid)
		return
	}
	c.Header("Cache-Control", "no-store")
	if err := h.devices.Logout(c.Request.Context(), req.RefreshToken, service.DesktopDeviceProofInput{
		DPoPProof: c.GetHeader("DPoP"),
		Method:    c.Request.Method,
		TargetURL: desktopRequestURL(c),
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}

func (h *DesktopDeviceHandler) List(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	devices, err := h.devices.ListDevices(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"devices": devices})
}

func (h *DesktopDeviceHandler) Revoke(c *gin.Context) {
	if h == nil || h.devices == nil {
		response.ErrorFrom(c, service.ErrServiceUnavailable)
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	actorDeviceID, _ := middleware2.GetDeviceIDFromContext(c)
	if err := h.devices.RevokeDeviceForActor(c.Request.Context(), subject.UserID, c.Param("device_id"), actorDeviceID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}

func parseDesktopScope(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.Fields(value), nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func writeDesktopTokenError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDesktopAuthorizationPending):
		writeDesktopOAuthError(c, http.StatusBadRequest, "authorization_pending", "the user has not approved this device yet")
	case errors.Is(err, service.ErrDesktopAuthorizationDenied):
		writeDesktopOAuthError(c, http.StatusBadRequest, "access_denied", "the user denied this device")
	case errors.Is(err, service.ErrDesktopAuthorizationExpired), errors.Is(err, service.ErrDesktopAuthorizationUsed):
		writeDesktopOAuthError(c, http.StatusBadRequest, "expired_token", "the device authorization code is expired or already used")
	case errors.Is(err, service.ErrDesktopProofInvalid):
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_grant", "the PKCE verifier or public key does not match")
	case errors.Is(err, service.ErrDesktopAudienceInvalid):
		// An audience mismatch is a client-bound grant error, not an outage. Do
		// not turn a malformed token request into a retryable 503 response.
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_grant", "the token audience does not match this desktop client")
	case errors.Is(err, service.ErrDesktopClientInvalid):
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_client", "the token client does not match this desktop client")
	case errors.Is(err, service.ErrDesktopDeviceKeyOwned), errors.Is(err, service.ErrDesktopDeviceAlreadyActive):
		writeDesktopOAuthError(c, http.StatusBadRequest, "invalid_grant", "this device key cannot be enrolled for the requested session")
	case errors.Is(err, service.ErrRefreshTokenInvalid), errors.Is(err, service.ErrRefreshTokenExpired), errors.Is(err, service.ErrRefreshTokenReused), errors.Is(err, service.ErrRefreshTokenFamilyRevoked), errors.Is(err, service.ErrTokenRevoked):
		writeDesktopOAuthError(c, http.StatusUnauthorized, "invalid_grant", "the refresh token is invalid or expired")
	case errors.Is(err, service.ErrUserNotActive), errors.Is(err, service.ErrDesktopDeviceRevoked):
		writeDesktopOAuthError(c, http.StatusUnauthorized, "invalid_grant", "the user or device is not active")
	default:
		writeDesktopOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "desktop authorization service unavailable")
	}
}

func writeDesktopOAuthError(c *gin.Context, status int, code, description string) {
	c.JSON(status, gin.H{"error": code, "error_description": description})
}
