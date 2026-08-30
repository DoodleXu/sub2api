package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// NewJWTAuthMiddleware 创建 JWT 认证中间件
func NewJWTAuthMiddleware(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
) JWTAuthMiddleware {
	return JWTAuthMiddleware(jwtAuth(authService, userService, userService, settingService, auditService))
}

// ProvideJWTAuthMiddleware is the Wire entry point.  The legacy constructor
// above intentionally remains four-argument source-compatible for lightweight
// integrations and tests; production wiring additionally injects the desktop
// session revocation checker.
func ProvideJWTAuthMiddleware(
	authService *service.AuthService,
	userService *service.UserService,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
	checker *service.DesktopDeviceService,
	cfg *config.Config,
) JWTAuthMiddleware {
	policy := desktopTransportPolicyFromConfig(cfg)
	return JWTAuthMiddleware(jwtAuthWithPolicy(authService, userService, userService, settingService, auditService, policy, checker))
}

type jwtUserReader interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

type userActivityToucher interface {
	TouchLastActiveForUser(ctx context.Context, user *service.User)
}

// jwtAuth JWT认证中间件实现
func jwtAuth(
	authService *service.AuthService,
	userService jwtUserReader,
	activityToucher userActivityToucher,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
	revocationCheckers ...service.DeviceSessionRevocationChecker,
) gin.HandlerFunc {
	return jwtAuthWithPolicy(authService, userService, activityToucher, settingService, auditService, DesktopTransportPolicy{}, revocationCheckers...)
}

func jwtAuthWithPolicy(
	authService *service.AuthService,
	userService jwtUserReader,
	activityToucher userActivityToucher,
	settingService *service.SettingService,
	auditService *service.AuditLogService,
	policy DesktopTransportPolicy,
	revocationCheckers ...service.DeviceSessionRevocationChecker,
) gin.HandlerFunc {
	var revocationChecker service.DeviceSessionRevocationChecker
	if len(revocationCheckers) > 0 {
		revocationChecker = revocationCheckers[0]
	}
	return func(c *gin.Context) {
		// 从Authorization header中提取token
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			AbortWithError(c, 401, "UNAUTHORIZED", "Authorization header is required")
			return
		}

		// Browser sessions use Bearer. RFC 9449-bound desktop sessions may use
		// DPoP; the claims check below prevents a regular web JWT from being
		// relabelled as DPoP.
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 {
			AbortWithError(c, 401, "INVALID_AUTH_HEADER", "Authorization header format must be 'Bearer {token}' or 'DPoP {token}'")
			return
		}
		authScheme := strings.ToLower(strings.TrimSpace(parts[0]))
		if authScheme != "bearer" && authScheme != "dpop" {
			AbortWithError(c, 401, "INVALID_AUTH_HEADER", "Authorization header format must be 'Bearer {token}' or 'DPoP {token}'")
			return
		}

		tokenString := strings.TrimSpace(parts[1])
		if tokenString == "" {
			AbortWithError(c, 401, "EMPTY_TOKEN", "Token cannot be empty")
			return
		}

		// 验证token
		claims, err := authService.ValidateToken(tokenString)
		if err != nil {
			if errors.Is(err, service.ErrTokenExpired) {
				AbortWithError(c, 401, "TOKEN_EXPIRED", "Token has expired")
				return
			}
			AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
			return
		}
		if authScheme == "dpop" && claims.DeviceID == "" {
			AbortWithError(c, 401, "INVALID_TOKEN", "DPoP authorization requires a desktop device token")
			return
		}
		if claims.DeviceID != "" && authScheme != "dpop" {
			// Sender-constrained access tokens use the RFC 9449 DPoP
			// authorization scheme. A valid proof header must not turn a relabelled
			// Bearer credential into an accepted desktop request.
			AbortWithError(c, 401, "DPOP_AUTHORIZATION_REQUIRED", "Desktop device tokens require the DPoP authorization scheme")
			return
		}
		if claims.DeviceID != "" && revocationChecker != nil {
			if !hasAudience(claims, service.DesktopAudience) {
				AbortWithError(c, 401, "INVALID_TOKEN", "Invalid desktop token audience")
				return
			}
			var revoked bool
			var checkErr error
			if sessionChecker, ok := revocationChecker.(service.DeviceSessionTokenChecker); ok {
				revoked, checkErr = sessionChecker.IsDeviceSessionRevokedForSession(c.Request.Context(), claims.DeviceID, claims.SessionID)
			} else {
				revoked, checkErr = revocationChecker.IsDeviceSessionRevoked(c.Request.Context(), claims.DeviceID)
			}
			if checkErr != nil {
				// Device tokens fail closed when the revocation store is unavailable.
				AbortWithError(c, 503, "DEVICE_SESSION_UNAVAILABLE", "Device session validation is temporarily unavailable")
				return
			}
			if revoked {
				AbortWithError(c, 401, "DEVICE_SESSION_REVOKED", "Device session has been revoked")
				return
			}
			if !verifyDesktopDPoP(c, claims, tokenString, revocationChecker, policy) {
				return
			}
		} else if claims.DeviceID != "" {
			// A desktop token must never silently fall back to bearer semantics when
			// the device session verifier was not wired. Fail closed during partial
			// deployments instead of weakening the key binding.
			AbortWithError(c, 503, "DEVICE_SESSION_UNAVAILABLE", "Device session validation is temporarily unavailable")
			return
		}

		// 从数据库获取最新的用户信息
		user, err := userService.GetByID(c.Request.Context(), claims.UserID)
		if err != nil {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User not found")
			return
		}

		// 检查用户状态
		if !user.IsActive() {
			AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
			return
		}

		// Security: Validate TokenVersion to ensure token hasn't been invalidated
		// This check ensures tokens issued before a password change are rejected
		if claims.TokenVersion != user.TokenVersion {
			AbortWithError(c, 401, "TOKEN_REVOKED", "Token has been revoked (password changed)")
			return
		}

		// 会话绑定校验：IP/UA 任一变化即撤销会话（功能可在系统设置中关闭）
		if !enforceSessionBinding(c, authService, settingService, auditService, claims) {
			return
		}

		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      user.ID,
			Concurrency: user.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), user.Role)
		c.Set(ContextKeyAuthEmail, user.Email)
		c.Set(ContextKeySessionID, claims.SessionID)
		if claims.DeviceID != "" {
			c.Set(string(ContextKeyDeviceID), claims.DeviceID)
		}
		if len(claims.Scopes) > 0 {
			c.Set(string(ContextKeyScopes), append([]string(nil), claims.Scopes...))
		}
		if activityToucher != nil {
			activityToucher.TouchLastActiveForUser(c.Request.Context(), user)
		}

		c.Next()
	}
}

func desktopTransportPolicyFromConfig(cfg *config.Config) DesktopTransportPolicy {
	if cfg == nil {
		return DesktopTransportPolicy{}
	}
	return DesktopTransportPolicyForConfig(cfg.Server.TrustedProxies, cfg.Server.FrontendURL)
}

func hasAudience(claims *service.JWTClaims, expected string) bool {
	if claims == nil || strings.TrimSpace(expected) == "" {
		return false
	}
	for _, audience := range claims.Audience {
		if strings.TrimSpace(audience) == expected {
			return true
		}
	}
	return false
}

// Deprecated: prefer GetAuthSubjectFromContext in auth_subject.go.
