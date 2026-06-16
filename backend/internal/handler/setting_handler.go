package handler

import (
	"html"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// SettingHandler 公开设置处理器（无需认证）
type SettingHandler struct {
	settingService           *service.SettingService
	notificationEmailService *service.NotificationEmailService
	version                  string
}

// NewSettingHandler 创建公开设置处理器
func NewSettingHandler(settingService *service.SettingService, version string) *SettingHandler {
	return &SettingHandler{
		settingService: settingService,
		version:        version,
	}
}

// SetNotificationEmailService attaches the public notification email service without
// changing the constructor signature used by existing tests.
func (h *SettingHandler) SetNotificationEmailService(notificationEmailService *service.NotificationEmailService) {
	h.notificationEmailService = notificationEmailService
}

// GetPublicSettings 获取公开设置
// GET /api/v1/settings/public
func (h *SettingHandler) GetPublicSettings(c *gin.Context) {
	settings, err := h.settingService.GetPublicSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PublicSettings{
		RegistrationEnabled:              settings.RegistrationEnabled,
		EmailVerifyEnabled:               settings.EmailVerifyEnabled,
		ForceEmailOnThirdPartySignup:     settings.ForceEmailOnThirdPartySignup,
		RegistrationEmailSuffixWhitelist: settings.RegistrationEmailSuffixWhitelist,
		PromoCodeEnabled:                 settings.PromoCodeEnabled,
		PasswordResetEnabled:             settings.PasswordResetEnabled,
		InvitationCodeEnabled:            settings.InvitationCodeEnabled,
		TotpEnabled:                      settings.TotpEnabled,
		LoginAgreementEnabled:            settings.LoginAgreementEnabled,
		LoginAgreementMode:               settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:          settings.LoginAgreementUpdatedAt,
		LoginAgreementRevision:           settings.LoginAgreementRevision,
		LoginAgreementDocuments:          publicLoginAgreementDocumentsToDTO(settings.LoginAgreementDocuments),
		TurnstileEnabled:                 settings.TurnstileEnabled,
		TurnstileSiteKey:                 settings.TurnstileSiteKey,
		SiteName:                         settings.SiteName,
		SiteLogo:                         settings.SiteLogo,
		SiteSubtitle:                     settings.SiteSubtitle,
		APIBaseURL:                       settings.APIBaseURL,
		ContactInfo:                      settings.ContactInfo,
		DocURL:                           settings.DocURL,
		HomeContent:                      settings.HomeContent,
		SiteLaunchAt:                     settings.SiteLaunchAt,
		HideCcsImportButton:              settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:      settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:          settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:             settings.TableDefaultPageSize,
		TablePageSizeOptions:             settings.TablePageSizeOptions,
		CustomMenuItems:                  dto.ParseUserVisibleMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                  dto.ParseCustomEndpoints(settings.CustomEndpoints),
		DingTalkOAuthEnabled:             settings.DingTalkOAuthEnabled,
		LinuxDoOAuthEnabled:              settings.LinuxDoOAuthEnabled,
		WeChatOAuthEnabled:               settings.WeChatOAuthEnabled,
		WeChatOAuthOpenEnabled:           settings.WeChatOAuthOpenEnabled,
		WeChatOAuthMPEnabled:             settings.WeChatOAuthMPEnabled,
		WeChatOAuthMobileEnabled:         settings.WeChatOAuthMobileEnabled,
		OIDCOAuthEnabled:                 settings.OIDCOAuthEnabled,
		OIDCOAuthProviderName:            settings.OIDCOAuthProviderName,
		GitHubOAuthEnabled:               settings.GitHubOAuthEnabled,
		GoogleOAuthEnabled:               settings.GoogleOAuthEnabled,
		BackendModeEnabled:               settings.BackendModeEnabled,
		PaymentEnabled:                   settings.PaymentEnabled,
		Version:                          h.version,
		BalanceLowNotifyEnabled:          settings.BalanceLowNotifyEnabled,
		AccountQuotaNotifyEnabled:        settings.AccountQuotaNotifyEnabled,
		BalanceLowNotifyThreshold:        settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:      settings.BalanceLowNotifyRechargeURL,

		ChannelMonitorEnabled:                settings.ChannelMonitorEnabled,
		ChannelMonitorDefaultIntervalSeconds: settings.ChannelMonitorDefaultIntervalSeconds,

		AvailableChannelsEnabled:  settings.AvailableChannelsEnabled,
		WebConsoleEnabled:         settings.WebConsoleEnabled,
		WebConsoleDefaultEndpoint: settings.WebConsoleDefaultEndpoint,

		AffiliateEnabled: settings.AffiliateEnabled,

		RiskControlEnabled: settings.RiskControlEnabled,

		AllowUserViewErrorRequests: settings.AllowUserViewErrorRequests,
	})
}

// UnsubscribeNotificationEmail handles optional notification email opt-outs.
// GET/POST /api/v1/settings/email-unsubscribe?token=...
func (h *SettingHandler) UnsubscribeNotificationEmail(c *gin.Context) {
	wantsJSON := notificationEmailUnsubscribeWantsJSON(c)
	if h.notificationEmailService == nil {
		notificationEmailUnsubscribeError(c, wantsJSON, http.StatusInternalServerError, "notification email service is not configured")
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		notificationEmailUnsubscribeError(c, wantsJSON, http.StatusBadRequest, "token is required")
		return
	}
	result, err := h.notificationEmailService.Unsubscribe(c.Request.Context(), token)
	if err != nil {
		notificationEmailUnsubscribeError(c, wantsJSON, http.StatusBadRequest, err.Error())
		return
	}
	if wantsJSON {
		response.Success(c, result)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(notificationEmailUnsubscribeHTML("Unsubscribed", "退订成功 / Unsubscribed", "已为 "+result.Email+" 退订 "+result.Event+" 可选邮件通知。")))
}

func notificationEmailUnsubscribeWantsJSON(c *gin.Context) bool {
	if strings.Contains(strings.ToLower(c.GetHeader("Accept")), "application/json") {
		return true
	}
	if strings.Contains(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		return true
	}
	return strings.EqualFold(c.Query("format"), "json")
}

func notificationEmailUnsubscribeError(c *gin.Context, wantsJSON bool, status int, message string) {
	if wantsJSON {
		if status >= http.StatusInternalServerError {
			response.InternalError(c, message)
			return
		}
		response.BadRequest(c, message)
		return
	}
	c.Data(status, "text/html; charset=utf-8", []byte(notificationEmailUnsubscribeHTML("Unsubscribe failed", "退订失败 / Unsubscribe failed", message)))
}

func notificationEmailUnsubscribeHTML(title, heading, message string) string {
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>" + html.EscapeString(title) + "</title></head><body style=\"margin:0;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;background:#f8fafc;color:#111827;\"><main style=\"max-width:560px;margin:12vh auto;padding:32px;background:white;border:1px solid #e5e7eb;border-radius:14px;box-shadow:0 20px 60px rgba(15,23,42,.08);\"><h1 style=\"margin:0 0 12px;font-size:24px;line-height:1.3;\">" + html.EscapeString(heading) + "</h1><p style=\"margin:0;color:#4b5563;line-height:1.7;\">" + html.EscapeString(message) + "</p></main></body></html>"
}

func publicLoginAgreementDocumentsToDTO(items []service.LoginAgreementDocument) []dto.LoginAgreementDocument {
	result := make([]dto.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		result = append(result, dto.LoginAgreementDocument{
			ID:        item.ID,
			Title:     item.Title,
			ContentMD: item.ContentMD,
		})
	}
	return result
}
