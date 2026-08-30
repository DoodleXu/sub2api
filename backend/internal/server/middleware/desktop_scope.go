package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// RejectDesktopToken protects operations that are intentionally outside the
// desktop consent surface (creating, editing, deleting or resetting API keys).
// Browser JWTs do not carry a DeviceID and remain unaffected.
func RejectDesktopToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := GetDeviceIDFromContext(c); ok {
			AbortWithError(c, http.StatusForbidden, "DESKTOP_OPERATION_NOT_ALLOWED", "This operation is not available to desktop sessions")
			return
		}
		c.Next()
	}
}

// RequireBrowserSession rejects desktop device tokens for account-security
// operations (password, identity, MFA and passkey changes). Browser JWTs do
// not carry ContextKeyDeviceID, so this is a no-op for the existing web panel.
// Keeping this check in middleware makes it difficult for a newly added
// sensitive handler to accidentally become reachable with a narrowly scoped
// desktop token.
func RequireBrowserSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, isDevice := GetDeviceIDFromContext(c); isDevice {
			AbortWithError(c, http.StatusForbidden, "BROWSER_SESSION_REQUIRED", "This operation requires an interactive browser session")
			return
		}
		c.Next()
	}
}

// RequireDesktopScope limits only desktop tokens. Browser JWTs do not carry a
// scope claim and retain the existing panel behavior; this keeps the rollout
// backwards compatible while preventing a desktop grant for one capability
// from becoming an all-API credential.
func RequireDesktopScope(scope string) gin.HandlerFunc {
	return RequireDesktopScopes(scope)
}

// RequireDesktopScopes requires every listed capability for a desktop token.
// This is useful for composite DTOs such as /user/profile, which includes the
// account balance alongside identity fields. Browser JWTs remain compatible
// and bypass scope checks as before.
func RequireDesktopScopes(required ...string) gin.HandlerFunc {
	need := make([]string, 0, len(required))
	seen := make(map[string]struct{}, len(required))
	for _, scope := range required {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		need = append(need, scope)
	}
	return func(c *gin.Context) {
		// Browser JWTs have neither device_id nor scopes. A desktop JWT is
		// identified by device_id even when a malformed/legacy token omits the
		// scope claim; such a token must fail closed instead of inheriting the
		// browser's unrestricted panel behavior.
		if _, isDevice := GetDeviceIDFromContext(c); !isDevice {
			c.Next()
			return
		}
		value, exists := c.Get(string(ContextKeyScopes))
		if !exists {
			AbortWithError(c, http.StatusForbidden, "DESKTOP_SCOPE_REQUIRED", "The desktop token does not grant this capability")
			return
		}
		scopes, ok := value.([]string)
		if !ok {
			AbortWithError(c, http.StatusForbidden, "DESKTOP_SCOPE_INVALID", "Invalid desktop token scope")
			return
		}
		for _, wanted := range need {
			granted := false
			for _, candidate := range scopes {
				if candidate == wanted {
					granted = true
					break
				}
			}
			if !granted {
				AbortWithError(c, http.StatusForbidden, "DESKTOP_SCOPE_REQUIRED", "The desktop token does not grant this capability")
				return
			}
		}
		c.Next()
	}
}

// RequireOwnDesktopDevice limits a state-changing device operation to the
// device represented by the current desktop JWT. Browser sessions do not set
// ContextKeyDeviceID and intentionally pass through so the existing browser
// account-management flow can continue to revoke any device belonging to the
// user. Keeping this check at the route boundary prevents a newly added
// handler from accidentally turning a profile-scoped desktop token into an
// account-wide session-revocation primitive.
func RequireOwnDesktopDevice(paramName string) gin.HandlerFunc {
	paramName = strings.TrimSpace(paramName)
	if paramName == "" {
		paramName = "device_id"
	}
	return func(c *gin.Context) {
		currentDeviceID, isDesktop := GetDeviceIDFromContext(c)
		if !isDesktop {
			// Browser JWTs have no device binding and retain the legacy ability to
			// manage any device under the authenticated account.
			c.Next()
			return
		}
		requestedDeviceID := c.Param(paramName)
		if requestedDeviceID == "" || subtle.ConstantTimeCompare([]byte(currentDeviceID), []byte(requestedDeviceID)) != 1 {
			AbortWithError(c, http.StatusForbidden, "DESKTOP_DEVICE_SELF_ONLY", "A desktop session may revoke only its own device")
			return
		}
		c.Next()
	}
}
