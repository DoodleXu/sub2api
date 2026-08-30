package routes

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/middleware"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RegisterAuthRoutes 注册认证相关路由
func RegisterAuthRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth servermiddleware.JWTAuthMiddleware,
	auditLog servermiddleware.AuditLogMiddleware,
	redisClient *redis.Client,
	settingService *service.SettingService,
	panelRateLimiter *servermiddleware.PanelRateLimiter,
	configs ...*config.Config,
) {
	// 创建速率限制器
	rateLimiter := middleware.NewRateLimiter(redisClient)
	var transportPolicy servermiddleware.DesktopTransportPolicy
	if len(configs) > 0 && configs[0] != nil {
		transportPolicy = servermiddleware.DesktopTransportPolicyForConfig(configs[0].Server.TrustedProxies, configs[0].Server.FrontendURL)
	}

	// Public capability discovery is deliberately outside the auth guard so a
	// fresh desktop client can bootstrap even before a user session exists.
	if h != nil && h.ClientCapabilities != nil && h.AsyncImage != nil {
		// Keep the bootstrap document aligned with the same runtime resolver used
		// by the async image routes. This avoids a client seeing `async_images=true`
		// here and then receiving a 404 from the gateway because object storage was
		// disabled or became unavailable.
		h.ClientCapabilities.SetImageTaskCapabilityResolver(func(context.Context) (bool, bool) {
			state := h.AsyncImage.Capability()
			return state.Enabled, state.Pollable
		})
	}
	if h != nil && h.ClientCapabilities != nil && settingService != nil {
		h.ClientCapabilities.SetBackendModeResolver(settingService.IsBackendModeEnabled)
	}
	if h != nil && h.ClientCapabilities != nil {
		v1.GET("/client/capabilities", h.ClientCapabilities.Get)
		// Keep generic profiles publicly discoverable. When api_key_id is
		// supplied, run the normal JWT/DPoP verifier first and require the
		// api_keys consent scope for desktop tokens; the handler then checks key
		// ownership before returning the non-secret profile metadata.
		v1.GET("/client/integration-profiles", requireHTTPSForKeySpecificProfile(transportPolicy), authenticateKeySpecificProfile(jwtAuth), servermiddleware.RequireDesktopScope("api_keys"), h.ClientCapabilities.IntegrationProfiles)
	}
	if h != nil && h.DesktopDevice != nil {
		// Canonical desktop protocol paths. /auth/device/* below remains as a
		// compatibility alias for existing helper builds.
		desktop := v1.Group("/desktop")
		desktop.Use(servermiddleware.BackendModeAuthGuard(settingService))
		desktop.Use(servermiddleware.RequireHTTPS(transportPolicy))
		desktop.POST("/device-authorizations", rateLimiter.LimitWithOptions("desktop-device-authorize", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.DesktopDevice.Authorize)
		desktop.POST("/token", rateLimiter.LimitWithOptions("desktop-device-token", 30, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.DesktopDevice.Token)
		desktop.POST("/logout", rateLimiter.LimitWithOptions("desktop-device-logout", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.DesktopDevice.Logout)
	}
	if h != nil && h.DesktopCheckout != nil {
		// Desktop checkout is authenticated and independently consented through
		// the billing scope. Browser JWTs remain compatible with the existing
		// panel behavior, while a device token cannot use this route without an
		// explicit billing grant.
		checkout := v1.Group("/desktop/checkout-sessions")
		checkout.Use(gin.HandlerFunc(jwtAuth))
		checkout.Use(servermiddleware.BackendModeUserGuard(settingService))
		checkout.Use(servermiddleware.RequireHTTPS(transportPolicy))
		checkout.Use(servermiddleware.RequireDesktopScope("billing"))
		if panelRateLimiter != nil {
			checkout.Use(panelRateLimiter.Global())
		}
		checkout.POST("", rateLimiter.LimitWithOptions("desktop-checkout-create", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.DesktopCheckout.Create)
		checkout.GET("/:session_id", rateLimiter.LimitWithOptions("desktop-checkout-get", 60, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.DesktopCheckout.Get)
		// Activation is deliberately restricted to an interactive browser JWT.
		// A desktop token may reserve and poll a session, but it must never receive
		// provider client secrets or payment redirect material.
		checkout.POST("/:session_id/activate",
			servermiddleware.RequireBrowserSession(),
			rateLimiter.LimitWithOptions("desktop-checkout-activate", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.DesktopCheckout.Activate,
		)
	}

	// 公开接口
	auth := v1.Group("/auth")
	auth.Use(servermiddleware.BackendModeAuthGuard(settingService))
	// 认证事件（登录/注册/2FA/token 刷新失败）入审计
	auth.Use(gin.HandlerFunc(auditLog))
	oauthCallbackGlobalRateLimit := rateLimiter.LimitWithOptions("oauth-callback-global", 120, time.Minute, middleware.RateLimitOptions{
		FailureMode: middleware.RateLimitFailClose,
	})
	oauthCallbackRateLimit := rateLimiter.LimitWithOptions("oauth-callback-provider", 30, time.Minute, middleware.RateLimitOptions{
		FailureMode: middleware.RateLimitFailClose,
		KeyFunc:     func(c *gin.Context) string { return c.FullPath() + ":" + c.ClientIP() },
	})
	{
		if h != nil && h.DesktopDevice != nil {
			device := auth.Group("/device")
			device.Use(servermiddleware.RequireHTTPS(transportPolicy))
			device.POST("/authorize", rateLimiter.LimitWithOptions("desktop-device-authorize", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}), h.DesktopDevice.Authorize)
			device.POST("/token", rateLimiter.LimitWithOptions("desktop-device-token", 30, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}), h.DesktopDevice.Token)
			device.POST("/logout", rateLimiter.LimitWithOptions("desktop-device-logout", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}), h.DesktopDevice.Logout)
		}
		// 注册/登录/2FA/验证码发送均属于高风险入口，增加服务端兜底限流（Redis 故障时 fail-close）
		auth.POST("/register", rateLimiter.LimitWithOptions("auth-register", 5, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.Register)
		auth.POST("/login", rateLimiter.LimitWithOptions("auth-login", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.Login)
		auth.POST("/login/2fa", rateLimiter.LimitWithOptions("auth-login-2fa", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.Login2FA)
		auth.POST("/passkey/login/begin", rateLimiter.LimitWithOptions("passkey-login-begin", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Passkey.BeginLogin)
		auth.POST("/passkey/login/finish", rateLimiter.LimitWithOptions("passkey-login-finish", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Passkey.FinishLogin)
		auth.POST("/send-verify-code", rateLimiter.LimitWithOptions("auth-send-verify-code", 5, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.SendVerifyCode)
		// Token刷新接口添加速率限制：每分钟最多 30 次（Redis 故障时 fail-close）
		auth.POST("/refresh", rateLimiter.LimitWithOptions("refresh-token", 30, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.RefreshToken)
		// 登出接口（公开，允许未认证用户调用以撤销Refresh Token）
		auth.POST("/logout", h.Auth.Logout)
		// 优惠码验证接口添加速率限制：每分钟最多 10 次（Redis 故障时 fail-close）
		auth.POST("/validate-promo-code", rateLimiter.LimitWithOptions("validate-promo", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.ValidatePromoCode)
		// 邀请码验证接口添加速率限制：每分钟最多 10 次（Redis 故障时 fail-close）
		auth.POST("/validate-invitation-code", rateLimiter.LimitWithOptions("validate-invitation", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.ValidateInvitationCode)
		// 忘记密码接口添加速率限制：每分钟最多 5 次（Redis 故障时 fail-close）
		auth.POST("/forgot-password", rateLimiter.LimitWithOptions("forgot-password", 5, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.ForgotPassword)
		// 重置密码接口添加速率限制：每分钟最多 10 次（Redis 故障时 fail-close）
		auth.POST("/reset-password", rateLimiter.LimitWithOptions("reset-password", 10, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.ResetPassword)
		auth.GET("/oauth/linuxdo/start", h.Auth.LinuxDoOAuthStart)
		auth.POST("/oauth/linuxdo/start", rateLimiter.LimitWithOptions("oauth-linuxdo-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.LinuxDoOAuthStart)
		auth.GET("/oauth/github/start", h.Auth.GitHubOAuthStart)
		auth.GET("/oauth/github/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.GitHubOAuthCallback)
		auth.POST("/oauth/github/start", rateLimiter.LimitWithOptions("oauth-github-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.GitHubOAuthStart)
		auth.POST("/oauth/github/complete-registration",
			rateLimiter.LimitWithOptions("oauth-github-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteGitHubOAuthRegistration,
		)
		auth.GET("/oauth/github/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.GitHubOAuthStart(c)
		})
		auth.GET("/oauth/google/start", h.Auth.GoogleOAuthStart)
		auth.GET("/oauth/google/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.GoogleOAuthCallback)
		auth.POST("/oauth/google/start", rateLimiter.LimitWithOptions("oauth-google-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.GoogleOAuthStart)
		auth.POST("/oauth/google/complete-registration",
			rateLimiter.LimitWithOptions("oauth-google-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteGoogleOAuthRegistration,
		)
		auth.GET("/oauth/google/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.GoogleOAuthStart(c)
		})
		auth.GET("/oauth/linuxdo/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.LinuxDoOAuthStart(c)
		})
		auth.GET("/oauth/linuxdo/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.LinuxDoOAuthCallback)
		auth.GET("/oauth/wechat/start", h.Auth.WeChatOAuthStart)
		auth.POST("/oauth/wechat/start", rateLimiter.LimitWithOptions("oauth-wechat-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.WeChatOAuthStart)
		auth.GET("/oauth/wechat/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.WeChatOAuthStart(c)
		})
		auth.GET("/oauth/wechat/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.WeChatOAuthCallback)
		auth.GET("/oauth/wechat/payment/start", h.Auth.WeChatPaymentOAuthStart)
		auth.GET("/oauth/wechat/payment/callback", h.Auth.WeChatPaymentOAuthCallback)
		auth.POST("/oauth/pending/exchange",
			rateLimiter.LimitWithOptions("oauth-pending-exchange", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.ExchangePendingOAuthCompletion,
		)
		auth.POST("/oauth/pending/send-verify-code",
			rateLimiter.LimitWithOptions("oauth-pending-send-verify-code", 5, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.SendPendingOAuthVerifyCode,
		)
		auth.POST("/oauth/pending/create-account",
			rateLimiter.LimitWithOptions("oauth-pending-create-account", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CreatePendingOAuthAccount,
		)
		auth.POST("/oauth/pending/bind-login",
			rateLimiter.LimitWithOptions("oauth-pending-bind-login", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.BindPendingOAuthLogin,
		)
		auth.POST("/oauth/linuxdo/complete-registration",
			rateLimiter.LimitWithOptions("oauth-linuxdo-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteLinuxDoOAuthRegistration,
		)
		auth.POST("/oauth/linuxdo/bind-login",
			rateLimiter.LimitWithOptions("oauth-linuxdo-bind-login", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.BindLinuxDoOAuthLogin,
		)
		auth.POST("/oauth/linuxdo/create-account",
			rateLimiter.LimitWithOptions("oauth-linuxdo-create-account", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CreateLinuxDoOAuthAccount,
		)
		auth.POST("/oauth/wechat/complete-registration",
			rateLimiter.LimitWithOptions("oauth-wechat-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteWeChatOAuthRegistration,
		)
		auth.POST("/oauth/wechat/bind-login",
			rateLimiter.LimitWithOptions("oauth-wechat-bind-login", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.BindWeChatOAuthLogin,
		)
		auth.POST("/oauth/wechat/create-account",
			rateLimiter.LimitWithOptions("oauth-wechat-create-account", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CreateWeChatOAuthAccount,
		)
		auth.GET("/oauth/oidc/start", h.Auth.OIDCOAuthStart)
		auth.POST("/oauth/oidc/start", rateLimiter.LimitWithOptions("oauth-oidc-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.OIDCOAuthStart)
		auth.GET("/oauth/oidc/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.OIDCOAuthStart(c)
		})
		auth.GET("/oauth/oidc/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.OIDCOAuthCallback)
		auth.POST("/oauth/oidc/complete-registration",
			rateLimiter.LimitWithOptions("oauth-oidc-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteOIDCOAuthRegistration,
		)
		auth.POST("/oauth/oidc/bind-login",
			rateLimiter.LimitWithOptions("oauth-oidc-bind-login", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.BindOIDCOAuthLogin,
		)
		auth.POST("/oauth/oidc/create-account",
			rateLimiter.LimitWithOptions("oauth-oidc-create-account", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CreateOIDCOAuthAccount,
		)
		auth.GET("/oauth/dingtalk/start", h.Auth.DingTalkOAuthStart)
		auth.POST("/oauth/dingtalk/start", rateLimiter.LimitWithOptions("oauth-dingtalk-start", 20, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailClose,
		}), h.Auth.DingTalkOAuthStart)
		auth.GET("/oauth/dingtalk/bind/start", func(c *gin.Context) {
			query := c.Request.URL.Query()
			query.Set("intent", "bind_current_user")
			c.Request.URL.RawQuery = query.Encode()
			h.Auth.DingTalkOAuthStart(c)
		})
		auth.GET("/oauth/dingtalk/callback", oauthCallbackGlobalRateLimit, oauthCallbackRateLimit, h.Auth.DingTalkOAuthCallback)
		auth.POST("/oauth/dingtalk/complete-registration",
			rateLimiter.LimitWithOptions("oauth-dingtalk-complete", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CompleteDingTalkOAuthRegistration,
		)
		auth.POST("/oauth/dingtalk/bind-login",
			rateLimiter.LimitWithOptions("oauth-dingtalk-bind-login", 20, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.BindDingTalkOAuthLogin,
		)
		auth.POST("/oauth/dingtalk/create-account",
			rateLimiter.LimitWithOptions("oauth-dingtalk-create-account", 10, time.Minute, middleware.RateLimitOptions{
				FailureMode: middleware.RateLimitFailClose,
			}),
			h.Auth.CreateDingTalkOAuthAccount,
		)
	}

	// 公开设置（无需认证）：每次请求都会查询 DB，按客户端 IP 兜底限流，
	// 防止匿名高频刷接口打爆数据库（反代内部地址会被自动跳过，不会误伤）。
	settings := v1.Group("/settings")
	settings.Use(panelRateLimiter.PublicIP())
	{
		settings.GET("/public", h.Setting.GetPublicSettings)
		settings.GET("/email-unsubscribe", h.Setting.UnsubscribeNotificationEmail)
		settings.POST("/email-unsubscribe", h.Setting.UnsubscribeNotificationEmail)
	}

	// 需要认证的当前用户信息
	authenticated := v1.Group("")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(servermiddleware.BackendModeUserGuard(settingService))
	// 面板全局按用户限流
	authenticated.Use(panelRateLimiter.Global())
	{
		authenticated.GET("/auth/me", servermiddleware.RequireDesktopScopes("profile", "balance"), h.Auth.GetCurrentUser)
		// 撤销所有会话（需要认证）
		authenticated.POST("/auth/revoke-all-sessions", servermiddleware.RequireBrowserSession(), h.Auth.RevokeAllSessions)
		authenticated.POST("/auth/oauth/bind-token", servermiddleware.RequireBrowserSession(), h.Auth.PrepareOAuthBindAccessTokenCookie)
	}
}

// authenticateKeySpecificProfile preserves the unauthenticated bootstrap form
// while making the query form an authenticated request. Calling jwtAuth from a
// middleware (rather than from the final handler) keeps Gin's handler index and
// c.Next semantics intact for both branches.
func authenticateKeySpecificProfile(jwtAuth servermiddleware.JWTAuthMiddleware) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, present := c.GetQuery("api_key_id"); !present {
			c.Next()
			return
		}
		gin.HandlerFunc(jwtAuth)(c)
	}
}

// requireHTTPSForKeySpecificProfile leaves the credential-free bootstrap form
// available to local/legacy callers, but protects the query form before any
// JWT or DPoP credential is accepted.  In particular, a browser Bearer token
// must not be sent over cleartext merely because the route is also public.
func requireHTTPSForKeySpecificProfile(policy servermiddleware.DesktopTransportPolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, present := c.GetQuery("api_key_id"); !present {
			c.Next()
			return
		}
		servermiddleware.RequireHTTPS(policy)(c)
	}
}
