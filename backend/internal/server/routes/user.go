package routes

import (
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	ratelimitmiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RegisterUserRoutes 注册用户相关路由（需要认证）
func RegisterUserRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth servermiddleware.JWTAuthMiddleware,
	auditLog servermiddleware.AuditLogMiddleware,
	redisClient *redis.Client,
	settingService *service.SettingService,
	panelRateLimiter *servermiddleware.PanelRateLimiter,
) {
	rateLimiter := ratelimitmiddleware.NewRateLimiter(redisClient)
	authenticated := v1.Group("")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(servermiddleware.BackendModeUserGuard(settingService))
	// 面板全局按用户限流：防止单个账号高频刷接口打爆数据库
	authenticated.Use(panelRateLimiter.Global())
	// 用户管理面变更类操作入审计（含 TOTP 启用/禁用、step-up 验证、密码修改等安全事件）。
	authenticated.Use(gin.HandlerFunc(auditLog))
	{
		// 用户接口
		user := authenticated.Group("/user")
		{
			if h != nil && h.DesktopDevice != nil {
				// Browser approval never returns credentials to the desktop client.
				// A device token may read its own device list, but it must not be
				// able to approve a second authorization request. Approval is an
				// interactive browser consent action and therefore explicitly
				// rejects all desktop JWTs.
				user.GET("/device/approval", servermiddleware.RequireBrowserSession(), h.DesktopDevice.ApprovalDetails)
				user.POST("/device/approve", servermiddleware.RequireBrowserSession(), h.DesktopDevice.Approve)
				devices := user.Group("/devices")
				devices.GET("", servermiddleware.RequireDesktopScope("profile"), h.DesktopDevice.List)
				devices.DELETE("/:device_id", servermiddleware.RequireDesktopScope("profile"), servermiddleware.RequireOwnDesktopDevice("device_id"), h.DesktopDevice.Revoke)
			}
			// The profile DTO includes the current account balance. Require both
			// consent scopes so a profile-only desktop grant cannot read balance
			// data through this composite response.
			user.GET("/profile", servermiddleware.RequireDesktopScopes("profile", "balance"), h.User.GetProfile)
			user.GET("/balance", servermiddleware.RequireDesktopScope("balance"), h.User.GetBalance)
			// A device grant must never be able to rotate the account password,
			// alter profile/identity bindings, or mutate MFA/passkey state. Those
			// operations remain available to the browser session only.
			user.PUT("/password", servermiddleware.RequireBrowserSession(), h.User.ChangePassword)
			user.PUT("", servermiddleware.RequireBrowserSession(), h.User.UpdateProfile)
			user.GET("/aff", servermiddleware.RequireDesktopScope("profile"), h.User.GetAffiliate)
			// Balance scope is read-only. Quota transfers remain an interactive
			// browser operation and are never exposed through a desktop grant.
			user.POST("/aff/transfer", servermiddleware.RequireBrowserSession(), h.User.TransferAffiliateQuota)
			user.POST("/account-bindings/email/send-code", servermiddleware.RequireBrowserSession(), h.User.SendEmailBindingCode)
			user.POST("/account-bindings/email", servermiddleware.RequireBrowserSession(), h.User.BindEmailIdentity)
			user.DELETE("/account-bindings/:provider", servermiddleware.RequireBrowserSession(), h.User.UnbindIdentity)
			user.POST("/auth-identities/bind/start", servermiddleware.RequireBrowserSession(), h.User.StartIdentityBinding)
			user.GET("/api-keys/:id/usage/daily", panelRateLimiter.Heavy(), servermiddleware.RequireDesktopScope("usage"), h.Usage.GetMyAPIKeyDailyUsage)
			user.GET("/platform-quotas", servermiddleware.RequireDesktopScope("usage"), h.User.GetMyPlatformQuotas)

			// 生图任务历史只按 JWT 用户归属读取；资产 URL 在读取时重新签发，
			// 不把 API Key 或对象键暴露给桌面客户端。
			imageTasks := user.Group("/image-tasks")
			imageTasks.Use(panelRateLimiter.Heavy())
			imageTasks.Use(servermiddleware.RequireDesktopScope("images"))
			{
				imageTasks.GET("", h.AsyncImage.ListUser)
				imageTasks.GET("/:task_id/assets/:index", h.AsyncImage.GetUserAsset)
				imageTasks.GET("/:task_id", h.AsyncImage.GetUser)
				imageTasks.DELETE("/:task_id", h.AsyncImage.DeleteUser)
			}

			user.GET("/checkin/status", servermiddleware.RequireDesktopScope("checkin"), h.DailyCheckin.Status)
			user.POST("/checkin", rateLimiter.LimitWithOptions("user-checkin", 6, time.Minute, ratelimitmiddleware.RateLimitOptions{
				FailureMode: ratelimitmiddleware.RateLimitFailClose,
				KeyFunc: func(c *gin.Context) string {
					subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
					if ok {
						return fmt.Sprintf("user:%d", subject.UserID)
					}
					return "ip:" + c.ClientIP()
				},
			}), servermiddleware.RequireDesktopScope("checkin"), h.DailyCheckin.CheckIn)

			// 通知邮箱管理
			notifyEmail := user.Group("/notify-email")
			{
				notifyEmail.Use(servermiddleware.RequireBrowserSession())
				notifyEmail.POST("/send-code", h.User.SendNotifyEmailCode)
				notifyEmail.POST("/verify", h.User.VerifyNotifyEmail)
				notifyEmail.PUT("/toggle", h.User.ToggleNotifyEmail)
				notifyEmail.DELETE("", h.User.RemoveNotifyEmail)
			}

			// TOTP 双因素认证
			totp := user.Group("/totp")
			{
				totp.Use(servermiddleware.RequireBrowserSession())
				totp.GET("/status", h.Totp.GetStatus)
				totp.GET("/verification-method", h.Totp.GetVerificationMethod)
				totp.POST("/send-code", h.Totp.SendVerifyCode)
				totp.POST("/setup", h.Totp.InitiateSetup)
				totp.POST("/enable", h.Totp.Enable)
				totp.POST("/disable", h.Totp.Disable)
				// 敏感操作二次验证：授予当前 JWT 会话一段时间的 step-up 权限。
				totp.POST("/step-up", h.Totp.StepUp)
			}

			passkeys := user.Group("/passkeys")
			{
				passkeys.Use(servermiddleware.RequireBrowserSession())
				passkeys.GET("", h.Passkey.List)
				passkeys.POST("/register/begin", h.Passkey.BeginRegistration)
				passkeys.POST("/register/finish", h.Passkey.FinishRegistration)
				passkeys.PATCH("/:id", h.Passkey.Rename)
				passkeys.DELETE("/:id", h.Passkey.Delete)
			}
		}

		// API Key管理
		keys := authenticated.Group("/keys")
		{
			keys.GET("", servermiddleware.RequireDesktopScope("api_keys"), h.APIKey.List)
			keys.GET("/:id", servermiddleware.RequireDesktopScope("api_keys"), h.APIKey.GetByID)
			keys.POST("", servermiddleware.RejectDesktopToken(), h.APIKey.Create)
			keys.PUT("/:id", servermiddleware.RejectDesktopToken(), h.APIKey.Update)
			keys.DELETE("/:id", servermiddleware.RejectDesktopToken(), h.APIKey.Delete)
		}

		// 用户可用分组（非管理员接口）
		groups := authenticated.Group("/groups")
		{
			groups.GET("/available", servermiddleware.RequireDesktopScope("profile"), h.APIKey.GetAvailableGroups)
			groups.GET("/rates", servermiddleware.RequireDesktopScope("profile"), h.APIKey.GetUserGroupRates)
		}

		// 用户可用渠道（非管理员接口）
		channels := authenticated.Group("/channels")
		{
			channels.GET("/available", servermiddleware.RequireDesktopScope("profile"), h.AvailableChannel.List)
		}

		// 使用记录（聚合统计属重查询，叠加更严格的按用户限流）
		usage := authenticated.Group("/usage")
		usage.Use(panelRateLimiter.Heavy())
		{
			usage.GET("", servermiddleware.RequireDesktopScope("usage"), h.Usage.List)
			usage.GET("/errors", servermiddleware.RequireDesktopScope("usage"), h.Usage.ListErrors)
			usage.GET("/errors/:id", servermiddleware.RequireDesktopScope("usage"), h.Usage.GetErrorDetail)
			usage.GET("/:id", servermiddleware.RequireDesktopScope("usage"), h.Usage.GetByID)
			usage.GET("/stats", servermiddleware.RequireDesktopScope("usage"), h.Usage.Stats)
			// User dashboard endpoints
			usage.GET("/dashboard/stats", servermiddleware.RequireDesktopScope("usage"), h.Usage.DashboardStats)
			usage.GET("/dashboard/trend", servermiddleware.RequireDesktopScope("usage"), h.Usage.DashboardTrend)
			usage.GET("/dashboard/models", servermiddleware.RequireDesktopScope("usage"), h.Usage.DashboardModels)
			usage.GET("/dashboard/snapshot-v2", servermiddleware.RequireDesktopScope("usage"), h.Usage.DashboardSnapshotV2)
			usage.POST("/dashboard/api-keys-usage", servermiddleware.RequireDesktopScope("usage"), h.Usage.DashboardAPIKeysUsage)
		}

		// 公告（用户可见）
		announcements := authenticated.Group("/announcements")
		{
			announcements.GET("", servermiddleware.RequireDesktopScope("profile"), h.Announcement.List)
			announcements.POST("/:id/read", servermiddleware.RequireDesktopScope("profile"), h.Announcement.MarkRead)
		}

		// 卡密兑换 changes account balance and is intentionally browser-only. The
		// desktop client receives only the dedicated hosted checkout session API.
		redeem := authenticated.Group("/redeem")
		{
			redeem.Use(servermiddleware.RequireBrowserSession())
			redeem.POST("", h.Redeem.Redeem)
			redeem.GET("/history", h.Redeem.GetHistory)
		}

		// Subscription listing and mutations can expose order/account state. Keep
		// the existing panel surface browser-only; desktop checkout is opaque.
		subscriptions := authenticated.Group("/subscriptions")
		{
			subscriptions.Use(servermiddleware.RequireBrowserSession())
			subscriptions.GET("", h.Subscription.List)
			subscriptions.GET("/active", h.Subscription.GetActive)
			subscriptions.GET("/progress", h.Subscription.GetProgress)
			subscriptions.GET("/summary", h.Subscription.GetSummary)
		}

		// 渠道监控（用户只读）
		monitors := authenticated.Group("/channel-monitors")
		{
			monitors.Use(servermiddleware.RequireDesktopScope("usage"))
			monitors.GET("", h.ChannelMonitor.List)
			monitors.GET("/:id/status", h.ChannelMonitor.GetStatus)
		}

		// V2 passive views require feature on + mode=v2.
		monitorV2 := authenticated.Group("/channel-monitor-v2")
		monitorV2.Use(panelRateLimiter.Heavy())
		monitorV2.Use(channelMonitorModeV2Guard(settingService))
		monitorV2.Use(servermiddleware.RequireDesktopScope("usage"))
		{
			monitorV2.GET("/dimensions", h.ChannelMonitorV2.Dimensions)
			monitorV2.GET("/snapshot", h.ChannelMonitorV2.Snapshot)
			monitorV2.GET("/models", h.ChannelMonitorV2.Models)
			monitorV2.GET("/matrix", h.ChannelMonitorV2.Matrix)
			monitorV2.GET("/users", h.ChannelMonitorV2.Users)
		}
	}
}
