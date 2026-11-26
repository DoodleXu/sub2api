package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// semverPattern 预编译 semver 格式校验正则
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// menuItemIDPattern validates custom menu item IDs: alphanumeric, hyphens, underscores only.
var menuItemIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// generateMenuItemID generates a short random hex ID for a custom menu item.
func generateMenuItemID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate menu item ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func scopesContainOpenID(scopes string) bool {
	for _, scope := range strings.Fields(strings.ToLower(strings.TrimSpace(scopes))) {
		if scope == "openid" {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// neutralizeCSVFormula prevents spreadsheet applications from evaluating
// user-controlled values that start with a formula marker.
func neutralizeCSVFormula(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

// SettingHandler 系统设置处理器
type SettingHandler struct {
	settingService           *service.SettingService
	emailService             *service.EmailService
	turnstileService         *service.TurnstileService
	aliyunCaptchaService     *service.AliyunCaptchaService
	opsService               *service.OpsService
	paymentConfigService     *service.PaymentConfigService
	paymentService           *service.PaymentService
	userAttributeService     *service.UserAttributeService
	notificationEmailService *service.NotificationEmailService
	notificationService      *service.NotificationService
	dailyCheckinService      *service.DailyCheckinService
	totpService              *service.TotpService
	userService              *service.UserService
}

// NewSettingHandler 创建系统设置处理器
func NewSettingHandler(settingService *service.SettingService, emailService *service.EmailService, turnstileService *service.TurnstileService, opsService *service.OpsService, paymentConfigService *service.PaymentConfigService, paymentService *service.PaymentService, userAttributeService *service.UserAttributeService, dailyCheckinService ...*service.DailyCheckinService) *SettingHandler {
	var checkinService *service.DailyCheckinService
	if len(dailyCheckinService) > 0 {
		checkinService = dailyCheckinService[0]
	}
	return &SettingHandler{
		settingService:       settingService,
		emailService:         emailService,
		turnstileService:     turnstileService,
		opsService:           opsService,
		paymentConfigService: paymentConfigService,
		paymentService:       paymentService,
		userAttributeService: userAttributeService,
		dailyCheckinService:  checkinService,
	}
}

// SetNotificationEmailService attaches the notification template service without changing
// the constructor signature used by existing unit tests.
func (h *SettingHandler) SetNotificationEmailService(notificationEmailService *service.NotificationEmailService) {
	h.notificationEmailService = notificationEmailService
}

// SetNotificationService attaches the lightweight multi-channel notification service.
func (h *SettingHandler) SetNotificationService(notificationService *service.NotificationService) {
	h.notificationService = notificationService
}

// SetAliyunCaptchaService attaches the Aliyun captcha credential validator without
// changing the constructor signature used by existing unit tests.
func (h *SettingHandler) SetAliyunCaptchaService(aliyunCaptchaService *service.AliyunCaptchaService) {
	h.aliyunCaptchaService = aliyunCaptchaService
}

// SetStepUpDeps attaches the services backing the step-up switch preconditions
// (enable requires the acting admin to have TOTP enabled; disable is itself a
// step-up gated operation), without changing the constructor signature used by
// existing unit tests.
func (h *SettingHandler) SetStepUpDeps(totpService *service.TotpService, userService *service.UserService) {
	h.totpService = totpService
	h.userService = userService
}

// GetSettings 获取所有系统设置
// GET /api/v1/admin/settings
func (h *SettingHandler) GetSettings(c *gin.Context) {
	settings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	authSourceDefaults, err := h.settingService.GetAuthSourceDefaultSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Check if ops monitoring is enabled (respects config.ops.enabled)
	opsEnabled := h.opsService != nil && h.opsService.IsMonitoringEnabled(c.Request.Context())
	defaultSubscriptions := make([]dto.DefaultSubscriptionSetting, 0, len(settings.DefaultSubscriptions))
	for _, sub := range settings.DefaultSubscriptions {
		defaultSubscriptions = append(defaultSubscriptions, dto.DefaultSubscriptionSetting{
			GroupID:      sub.GroupID,
			ValidityDays: sub.ValidityDays,
		})
	}

	// Load payment config
	var paymentCfg *service.PaymentConfig
	if h.paymentConfigService != nil {
		paymentCfg, _ = h.paymentConfigService.GetPaymentConfig(c.Request.Context())
	}
	if paymentCfg == nil {
		paymentCfg = &service.PaymentConfig{}
	}
	passkeyConfigured, passkeyRPID, passkeyRPOrigins := h.settingService.PasskeyConfiguration()

	payload := dto.SystemSettings{
		RegistrationEnabled:                                    settings.RegistrationEnabled,
		EmailVerifyEnabled:                                     settings.EmailVerifyEnabled,
		RegistrationEmailSuffixWhitelist:                       settings.RegistrationEmailSuffixWhitelist,
		RegistrationEmailDomainQuotaEnabled:                    settings.RegistrationEmailDomainQuotaEnabled,
		PromoCodeEnabled:                                       settings.PromoCodeEnabled,
		PasswordResetEnabled:                                   settings.PasswordResetEnabled,
		FrontendURL:                                            settings.FrontendURL,
		InvitationCodeEnabled:                                  settings.InvitationCodeEnabled,
		TotpEnabled:                                            settings.TotpEnabled,
		TotpEncryptionKeyConfigured:                            h.settingService.IsTotpEncryptionKeyConfigured(),
		PasskeyEnabled:                                         settings.PasskeyEnabled,
		PasskeyConfigured:                                      passkeyConfigured,
		PasskeyRPID:                                            passkeyRPID,
		PasskeyRPOrigins:                                       passkeyRPOrigins,
		SessionBindingEnabled:                                  settings.SessionBindingEnabled,
		StepUpEnabled:                                          settings.StepUpEnabled,
		AuditLogRetentionDays:                                  settings.AuditLogRetentionDays,
		LoginAgreementEnabled:                                  settings.LoginAgreementEnabled,
		LoginAgreementMode:                                     settings.LoginAgreementMode,
		LoginAgreementUpdatedAt:                                settings.LoginAgreementUpdatedAt,
		LoginAgreementDocuments:                                loginAgreementDocumentsToDTO(settings.LoginAgreementDocuments),
		SMTPHost:                                               settings.SMTPHost,
		SMTPPort:                                               settings.SMTPPort,
		SMTPUsername:                                           settings.SMTPUsername,
		SMTPPasswordConfigured:                                 settings.SMTPPasswordConfigured,
		SMTPFrom:                                               settings.SMTPFrom,
		SMTPFromName:                                           settings.SMTPFromName,
		SMTPUseTLS:                                             settings.SMTPUseTLS,
		TurnstileEnabled:                                       settings.TurnstileEnabled,
		TurnstileSiteKey:                                       settings.TurnstileSiteKey,
		TurnstileSecretKeyConfigured:                           settings.TurnstileSecretKeyConfigured,
		TencentCaptchaEnabled:                                  settings.TencentCaptchaEnabled,
		TencentCaptchaAppID:                                    settings.TencentCaptchaAppID,
		TencentCaptchaAppSecretKeyConfigured:                   settings.TencentCaptchaAppSecretKeyConfigured,
		TencentCaptchaCloudSecretIDConfigured:                  settings.TencentCaptchaCloudSecretIDConfigured,
		TencentCaptchaCloudSecretKeyConfigured:                 settings.TencentCaptchaCloudSecretKeyConfigured,
		TencentCaptchaRegion:                                   settings.TencentCaptchaRegion,
		AliyunCaptchaEnabled:                                   settings.AliyunCaptchaEnabled,
		AliyunCaptchaAccessKeyID:                               settings.AliyunCaptchaAccessKeyID,
		AliyunCaptchaAccessKeySecretConfigured:                 settings.AliyunCaptchaAccessKeySecretConfigured,
		AliyunCaptchaSceneID:                                   settings.AliyunCaptchaSceneID,
		AliyunCaptchaPrefix:                                    settings.AliyunCaptchaPrefix,
		AliyunCaptchaRegion:                                    settings.AliyunCaptchaRegion,
		APIKeyACLTrustForwardedIP:                              settings.APIKeyACLTrustForwardedIP,
		ForwardedClientIPHeaders:                               settings.ForwardedClientIPHeaders,
		LinuxDoConnectEnabled:                                  settings.LinuxDoConnectEnabled,
		LinuxDoConnectClientID:                                 settings.LinuxDoConnectClientID,
		LinuxDoConnectClientSecretConfigured:                   settings.LinuxDoConnectClientSecretConfigured,
		LinuxDoConnectRedirectURL:                              settings.LinuxDoConnectRedirectURL,
		DingTalkConnectEnabled:                                 settings.DingTalkConnectEnabled,
		DingTalkConnectClientID:                                settings.DingTalkConnectClientID,
		DingTalkConnectClientSecretConfigured:                  settings.DingTalkConnectClientSecretConfigured,
		DingTalkConnectRedirectURL:                             settings.DingTalkConnectRedirectURL,
		DingTalkConnectCorpRestrictionPolicy:                   settings.DingTalkConnectCorpRestrictionPolicy,
		DingTalkConnectInternalCorpID:                          settings.DingTalkConnectInternalCorpID,
		DingTalkConnectBypassRegistration:                      settings.DingTalkConnectBypassRegistration,
		DingTalkConnectSyncCorpEmail:                           settings.DingTalkConnectSyncCorpEmail,
		DingTalkConnectSyncDisplayName:                         settings.DingTalkConnectSyncDisplayName,
		DingTalkConnectSyncDept:                                settings.DingTalkConnectSyncDept,
		DingTalkConnectSyncCorpEmailAttrKey:                    settings.DingTalkConnectSyncCorpEmailAttrKey,
		DingTalkConnectSyncDisplayNameAttrKey:                  settings.DingTalkConnectSyncDisplayNameAttrKey,
		DingTalkConnectSyncDeptAttrKey:                         settings.DingTalkConnectSyncDeptAttrKey,
		DingTalkConnectSyncCorpEmailAttrName:                   settings.DingTalkConnectSyncCorpEmailAttrName,
		DingTalkConnectSyncDisplayNameAttrName:                 settings.DingTalkConnectSyncDisplayNameAttrName,
		DingTalkConnectSyncDeptAttrName:                        settings.DingTalkConnectSyncDeptAttrName,
		WeChatConnectEnabled:                                   settings.WeChatConnectEnabled,
		WeChatConnectAppID:                                     settings.WeChatConnectAppID,
		WeChatConnectAppSecretConfigured:                       settings.WeChatConnectAppSecretConfigured,
		WeChatConnectOpenAppID:                                 settings.WeChatConnectOpenAppID,
		WeChatConnectOpenAppSecretConfigured:                   settings.WeChatConnectOpenAppSecretConfigured,
		WeChatConnectMPAppID:                                   settings.WeChatConnectMPAppID,
		WeChatConnectMPAppSecretConfigured:                     settings.WeChatConnectMPAppSecretConfigured,
		WeChatConnectMobileAppID:                               settings.WeChatConnectMobileAppID,
		WeChatConnectMobileAppSecretConfigured:                 settings.WeChatConnectMobileAppSecretConfigured,
		WeChatConnectOpenEnabled:                               settings.WeChatConnectOpenEnabled,
		WeChatConnectMPEnabled:                                 settings.WeChatConnectMPEnabled,
		WeChatConnectMobileEnabled:                             settings.WeChatConnectMobileEnabled,
		WeChatConnectMode:                                      settings.WeChatConnectMode,
		WeChatConnectScopes:                                    settings.WeChatConnectScopes,
		WeChatConnectRedirectURL:                               settings.WeChatConnectRedirectURL,
		WeChatConnectFrontendRedirectURL:                       settings.WeChatConnectFrontendRedirectURL,
		OIDCConnectEnabled:                                     settings.OIDCConnectEnabled,
		OIDCConnectProviderName:                                settings.OIDCConnectProviderName,
		OIDCConnectClientID:                                    settings.OIDCConnectClientID,
		OIDCConnectClientSecretConfigured:                      settings.OIDCConnectClientSecretConfigured,
		OIDCConnectIssuerURL:                                   settings.OIDCConnectIssuerURL,
		OIDCConnectDiscoveryURL:                                settings.OIDCConnectDiscoveryURL,
		OIDCConnectAuthorizeURL:                                settings.OIDCConnectAuthorizeURL,
		OIDCConnectTokenURL:                                    settings.OIDCConnectTokenURL,
		OIDCConnectUserInfoURL:                                 settings.OIDCConnectUserInfoURL,
		OIDCConnectJWKSURL:                                     settings.OIDCConnectJWKSURL,
		OIDCConnectScopes:                                      settings.OIDCConnectScopes,
		OIDCConnectRedirectURL:                                 settings.OIDCConnectRedirectURL,
		OIDCConnectFrontendRedirectURL:                         settings.OIDCConnectFrontendRedirectURL,
		OIDCConnectTokenAuthMethod:                             settings.OIDCConnectTokenAuthMethod,
		OIDCConnectUsePKCE:                                     settings.OIDCConnectUsePKCE,
		OIDCConnectValidateIDToken:                             settings.OIDCConnectValidateIDToken,
		OIDCConnectAllowedSigningAlgs:                          settings.OIDCConnectAllowedSigningAlgs,
		OIDCConnectClockSkewSeconds:                            settings.OIDCConnectClockSkewSeconds,
		OIDCConnectRequireEmailVerified:                        settings.OIDCConnectRequireEmailVerified,
		OIDCConnectUserInfoEmailPath:                           settings.OIDCConnectUserInfoEmailPath,
		OIDCConnectUserInfoIDPath:                              settings.OIDCConnectUserInfoIDPath,
		OIDCConnectUserInfoUsernamePath:                        settings.OIDCConnectUserInfoUsernamePath,
		GitHubOAuthEnabled:                                     settings.GitHubOAuthEnabled,
		GitHubOAuthClientID:                                    settings.GitHubOAuthClientID,
		GitHubOAuthClientSecretConfigured:                      settings.GitHubOAuthClientSecretConfigured,
		GitHubOAuthRedirectURL:                                 settings.GitHubOAuthRedirectURL,
		GitHubOAuthFrontendRedirectURL:                         settings.GitHubOAuthFrontendRedirectURL,
		GoogleOAuthEnabled:                                     settings.GoogleOAuthEnabled,
		GoogleOAuthClientID:                                    settings.GoogleOAuthClientID,
		GoogleOAuthClientSecretConfigured:                      settings.GoogleOAuthClientSecretConfigured,
		GoogleOAuthRedirectURL:                                 settings.GoogleOAuthRedirectURL,
		GoogleOAuthFrontendRedirectURL:                         settings.GoogleOAuthFrontendRedirectURL,
		SiteName:                                               settings.SiteName,
		SiteLogo:                                               settings.SiteLogo,
		SiteSubtitle:                                           settings.SiteSubtitle,
		APIBaseURL:                                             settings.APIBaseURL,
		ContactInfo:                                            settings.ContactInfo,
		DocURL:                                                 settings.DocURL,
		HomeContent:                                            settings.HomeContent,
		CompactHomeEnabled:                                     settings.CompactHomeEnabled,
		HideCcsImportButton:                                    settings.HideCcsImportButton,
		PurchaseSubscriptionEnabled:                            settings.PurchaseSubscriptionEnabled,
		PurchaseSubscriptionURL:                                settings.PurchaseSubscriptionURL,
		TableDefaultPageSize:                                   settings.TableDefaultPageSize,
		TablePageSizeOptions:                                   settings.TablePageSizeOptions,
		CustomMenuItems:                                        dto.ParseCustomMenuItems(settings.CustomMenuItems),
		CustomEndpoints:                                        dto.ParseCustomEndpoints(settings.CustomEndpoints),
		ClarityEnabled:                                         settings.ClarityEnabled,
		ClarityProjectID:                                       settings.ClarityProjectID,
		DefaultConcurrency:                                     settings.DefaultConcurrency,
		DefaultBalance:                                         settings.DefaultBalance,
		RiskControlEnabled:                                     settings.RiskControlEnabled,
		CyberSessionBlockEnabled:                               settings.CyberSessionBlockEnabled,
		CyberPolicyUserAllowlist:                               settings.CyberPolicyUserAllowlist,
		CyberSessionBlockTTLSeconds:                            settings.CyberSessionBlockTTLSeconds,
		AffiliateRebateRate:                                    settings.AffiliateRebateRate,
		AffiliateRebateFreezeHours:                             settings.AffiliateRebateFreezeHours,
		AffiliateRebateDurationDays:                            settings.AffiliateRebateDurationDays,
		AffiliateRebatePerInviteeCap:                           settings.AffiliateRebatePerInviteeCap,
		AdminRechargeRebateEnabled:                             settings.AdminRechargeRebateEnabled,
		DefaultUserRPMLimit:                                    settings.DefaultUserRPMLimit,
		DefaultSubscriptions:                                   defaultSubscriptions,
		EnableModelFallback:                                    settings.EnableModelFallback,
		FallbackModelAnthropic:                                 settings.FallbackModelAnthropic,
		FallbackModelOpenAI:                                    settings.FallbackModelOpenAI,
		FallbackModelGemini:                                    settings.FallbackModelGemini,
		FallbackModelAntigravity:                               settings.FallbackModelAntigravity,
		EnableIdentityPatch:                                    settings.EnableIdentityPatch,
		IdentityPatchPrompt:                                    settings.IdentityPatchPrompt,
		OpsMonitoringEnabled:                                   opsEnabled && settings.OpsMonitoringEnabled,
		OpsRealtimeMonitoringEnabled:                           settings.OpsRealtimeMonitoringEnabled,
		OpsQueryModeDefault:                                    settings.OpsQueryModeDefault,
		OpsMetricsIntervalSeconds:                              settings.OpsMetricsIntervalSeconds,
		MinClaudeCodeVersion:                                   settings.MinClaudeCodeVersion,
		MaxClaudeCodeVersion:                                   settings.MaxClaudeCodeVersion,
		AllowUngroupedKeyScheduling:                            settings.AllowUngroupedKeyScheduling,
		BackendModeEnabled:                                     settings.BackendModeEnabled,
		OpenAITTFTMode:                                         settings.OpenAITTFTMode,
		EnableFingerprintUnification:                           settings.EnableFingerprintUnification,
		EnableMetadataPassthrough:                              settings.EnableMetadataPassthrough,
		EnableCCHSigning:                                       settings.EnableCCHSigning,
		EnableClaudeOAuthSystemPromptInjection:                 settings.EnableClaudeOAuthSystemPromptInjection,
		ClaudeOAuthSystemPrompt:                                settings.ClaudeOAuthSystemPrompt,
		ClaudeOAuthSystemPromptBlocks:                          settings.ClaudeOAuthSystemPromptBlocks,
		EnableAnthropicCacheTTL1hInjection:                     settings.EnableAnthropicCacheTTL1hInjection,
		RewriteMessageCacheControl:                             settings.RewriteMessageCacheControl,
		EnableClientDatelineNormalization:                      settings.EnableClientDatelineNormalization,
		AntigravityUserAgentVersion:                            settings.AntigravityUserAgentVersion,
		OpenAICodexUserAgent:                                   settings.OpenAICodexUserAgent,
		OpenAICodexClientVersion:                               settings.OpenAICodexClientVersion,
		OpenAICodexClientVersionSynced:                         settings.OpenAICodexClientVersionSynced,
		OpenAICodexVersionAutoSyncEnabled:                      settings.OpenAICodexVersionAutoSyncEnabled,
		ClaudeCodeClientVersion:                                settings.ClaudeCodeClientVersion,
		ClaudeCodeClientVersionSynced:                          settings.ClaudeCodeClientVersionSynced,
		ClaudeCodeVersionAutoSyncEnabled:                       settings.ClaudeCodeVersionAutoSyncEnabled,
		MinCodexVersion:                                        settings.MinCodexVersion,
		MaxCodexVersion:                                        settings.MaxCodexVersion,
		CodexCLIOnlyBlacklist:                                  settings.CodexCLIOnlyBlacklist,
		CodexCLIOnlyWhitelist:                                  settings.CodexCLIOnlyWhitelist,
		CodexCLIOnlyAllowAppServerClients:                      settings.CodexCLIOnlyAllowAppServerClients,
		CodexCLIOnlyEngineFingerprintSignals:                   settings.CodexCLIOnlyEngineFingerprintSignals,
		WebSearchEmulationEnabled:                              settings.WebSearchEmulationEnabled,
		PaymentVisibleMethodAlipaySource:                       settings.PaymentVisibleMethodAlipaySource,
		PaymentVisibleMethodWxpaySource:                        settings.PaymentVisibleMethodWxpaySource,
		PaymentVisibleMethodAlipayEnabled:                      settings.PaymentVisibleMethodAlipayEnabled,
		PaymentVisibleMethodWxpayEnabled:                       settings.PaymentVisibleMethodWxpayEnabled,
		OpenAILowUpstreamRatePriorityEnabled:                   settings.OpenAILowUpstreamRatePriorityEnabled,
		OpenAIOAuthSchedulingRateMultiplier:                    settings.OpenAIOAuthSchedulingRateMultiplier,
		OpenAISchedulingUSDToCNYRate:                           settings.OpenAISchedulingUSDToCNYRate,
		AccountQualitySchedulingEnabled:                        settings.AccountQualitySchedulingEnabled,
		OpenAIAdvancedSchedulerEnabled:                         settings.OpenAIAdvancedSchedulerEnabled,
		OpenAIAdvancedSchedulerStickyWeightedEnabled:           settings.OpenAIAdvancedSchedulerStickyWeightedEnabled,
		OpenAIAdvancedSchedulerSubscriptionPriorityEnabled:     settings.OpenAIAdvancedSchedulerSubscriptionPriorityEnabled,
		OpenAIAdvancedSchedulerLBTopK:                          settings.OpenAIAdvancedSchedulerLBTopK,
		OpenAIAdvancedSchedulerWeightPriority:                  settings.OpenAIAdvancedSchedulerWeightPriority,
		OpenAIAdvancedSchedulerWeightLoad:                      settings.OpenAIAdvancedSchedulerWeightLoad,
		OpenAIAdvancedSchedulerWeightQueue:                     settings.OpenAIAdvancedSchedulerWeightQueue,
		OpenAIAdvancedSchedulerWeightErrorRate:                 settings.OpenAIAdvancedSchedulerWeightErrorRate,
		OpenAIAdvancedSchedulerWeightTTFT:                      settings.OpenAIAdvancedSchedulerWeightTTFT,
		OpenAIAdvancedSchedulerWeightReset:                     settings.OpenAIAdvancedSchedulerWeightReset,
		OpenAIAdvancedSchedulerWeightQuotaHeadroom:             settings.OpenAIAdvancedSchedulerWeightQuotaHeadroom,
		OpenAIAdvancedSchedulerWeightUpstreamCost:              settings.OpenAIAdvancedSchedulerWeightUpstreamCost,
		OpenAIAdvancedSchedulerWeightPreviousResponse:          settings.OpenAIAdvancedSchedulerWeightPreviousResponse,
		OpenAIAdvancedSchedulerWeightSessionSticky:             settings.OpenAIAdvancedSchedulerWeightSessionSticky,
		OpenAIAdvancedSchedulerEffectiveLBTopK:                 settings.OpenAIAdvancedSchedulerEffectiveLBTopK,
		OpenAIAdvancedSchedulerEffectiveWeightPriority:         settings.OpenAIAdvancedSchedulerEffectiveWeightPriority,
		OpenAIAdvancedSchedulerEffectiveWeightLoad:             settings.OpenAIAdvancedSchedulerEffectiveWeightLoad,
		OpenAIAdvancedSchedulerEffectiveWeightQueue:            settings.OpenAIAdvancedSchedulerEffectiveWeightQueue,
		OpenAIAdvancedSchedulerEffectiveWeightErrorRate:        settings.OpenAIAdvancedSchedulerEffectiveWeightErrorRate,
		OpenAIAdvancedSchedulerEffectiveWeightTTFT:             settings.OpenAIAdvancedSchedulerEffectiveWeightTTFT,
		OpenAIAdvancedSchedulerEffectiveWeightReset:            settings.OpenAIAdvancedSchedulerEffectiveWeightReset,
		OpenAIAdvancedSchedulerEffectiveWeightQuotaHeadroom:    settings.OpenAIAdvancedSchedulerEffectiveWeightQuotaHeadroom,
		OpenAIAdvancedSchedulerEffectiveWeightUpstreamCost:     settings.OpenAIAdvancedSchedulerEffectiveWeightUpstreamCost,
		OpenAIAdvancedSchedulerEffectiveWeightPreviousResponse: settings.OpenAIAdvancedSchedulerEffectiveWeightPreviousResponse,
		OpenAIAdvancedSchedulerEffectiveWeightSessionSticky:    settings.OpenAIAdvancedSchedulerEffectiveWeightSessionSticky,
		BalanceLowNotifyEnabled:                                settings.BalanceLowNotifyEnabled,
		BalanceLowNotifyThreshold:                              settings.BalanceLowNotifyThreshold,
		BalanceLowNotifyRechargeURL:                            settings.BalanceLowNotifyRechargeURL,
		SubscriptionExpiryNotifyEnabled:                        settings.SubscriptionExpiryNotifyEnabled,
		AccountQuotaNotifyEnabled:                              settings.AccountQuotaNotifyEnabled,
		AccountQuotaNotifyEmails:                               dto.NotifyEmailEntriesFromService(settings.AccountQuotaNotifyEmails),
		PaymentEnabled:                                         paymentCfg.Enabled,
		PaymentMinAmount:                                       paymentCfg.MinAmount,
		PaymentMaxAmount:                                       paymentCfg.MaxAmount,
		PaymentDailyLimit:                                      paymentCfg.DailyLimit,
		PaymentOrderTimeoutMin:                                 paymentCfg.OrderTimeoutMin,
		PaymentMaxPendingOrders:                                paymentCfg.MaxPendingOrders,
		PaymentEnabledTypes:                                    paymentCfg.EnabledTypes,
		PaymentBalanceDisabled:                                 paymentCfg.BalanceDisabled,
		PaymentBalanceRechargeMultiplier:                       paymentCfg.BalanceRechargeMultiplier,
		PaymentSubscriptionUSDToCNYRate:                        paymentCfg.SubscriptionUSDToCNYRate,
		PaymentRechargeFeeRate:                                 paymentCfg.RechargeFeeRate,
		PaymentRechargeGiftEnabled:                             paymentCfg.RechargeGiftEnabled,
		PaymentRechargeGiftTiers:                               paymentCfg.RechargeGiftTiers,
		PaymentRechargeBonusTiers:                              rechargeBonusTiersToDTO(paymentCfg.RechargeBonusTiers),
		PaymentRechargeBonusMode:                               rechargeBonusModeToDTO(paymentCfg.RechargeBonusMode),
		PaymentRechargeBonusNotice:                             paymentCfg.RechargeBonusNotice,
		PaymentLoadBalanceStrat:                                paymentCfg.LoadBalanceStrategy,
		PaymentProductNamePrefix:                               paymentCfg.ProductNamePrefix,
		PaymentProductNameSuffix:                               paymentCfg.ProductNameSuffix,
		PaymentHelpImageURL:                                    paymentCfg.HelpImageURL,
		PaymentHelpText:                                        paymentCfg.HelpText,
		PaymentCancelRateLimitEnabled:                          paymentCfg.CancelRateLimitEnabled,
		PaymentCancelRateLimitMax:                              paymentCfg.CancelRateLimitMax,
		PaymentCancelRateLimitWindow:                           paymentCfg.CancelRateLimitWindow,
		PaymentCancelRateLimitUnit:                             paymentCfg.CancelRateLimitUnit,
		PaymentCancelRateLimitMode:                             paymentCfg.CancelRateLimitMode,
		PaymentAlipayForceQRCode:                               paymentCfg.AlipayForceQRCode,
		PaymentAlipayMobilePrecreateDeepLink:                   paymentCfg.AlipayMobilePrecreateDeepLink,

		ChannelMonitorEnabled:                settings.ChannelMonitorEnabled,
		ChannelMonitorMode:                   settings.ChannelMonitorMode,
		ChannelMonitorDefaultIntervalSeconds: settings.ChannelMonitorDefaultIntervalSeconds,
		ChannelMonitorHideThroughput:         settings.ChannelMonitorHideThroughput,
		ChannelMonitorShowQuota:              settings.ChannelMonitorShowQuota,
		ChannelMonitorHideUserRanking:        settings.ChannelMonitorHideUserRanking,

		GrokDefaultTextModel:           settings.GrokDefaultTextModel,
		GrokCrossClientModelMapEnabled: settings.GrokCrossClientModelMapEnabled,
		GrokDefaultBaseURLMode:         settings.GrokDefaultBaseURLMode,

		AvailableChannelsEnabled:        settings.AvailableChannelsEnabled,
		SubscriptionEnabled:             settings.SubscriptionEnabled,
		WebConsoleEnabled:               settings.WebConsoleEnabled,
		WebConsoleDefaultEndpoint:       settings.WebConsoleDefaultEndpoint,
		DailyCheckinEnabled:             settings.DailyCheckinEnabled,
		DailyCheckinRequiredUsageUSD:    settings.DailyCheckinRequiredUsageUSD,
		DailyCheckinUsageScope:          settings.DailyCheckinUsageScope,
		DailyCheckinRewardMinUSD:        settings.DailyCheckinRewardMinUSD,
		DailyCheckinRewardMaxUSD:        settings.DailyCheckinRewardMaxUSD,
		DailyCheckinDailyBudgetUSD:      settings.DailyCheckinDailyBudgetUSD,
		DailyCheckinMonthlyBudgetUSD:    settings.DailyCheckinMonthlyBudgetUSD,
		DailyCheckinUserMonthlyLimitUSD: settings.DailyCheckinUserMonthlyLimitUSD,
		DailyCheckinBudgetFallbackUSD:   settings.DailyCheckinBudgetFallbackUSD,
		DailyCheckinBudgetFallbackText:  settings.DailyCheckinBudgetFallbackText,
		DailyCheckinRewardTiers:         settings.DailyCheckinRewardTiers,
		DailyCheckinStreakEnabled:       settings.DailyCheckinStreakEnabled,
		DailyCheckinStreakScope:         settings.DailyCheckinStreakScope,
		DailyCheckinStreakMultipliers:   settings.DailyCheckinStreakMultipliers,
		DailyCheckinCritEnabled:         settings.DailyCheckinCritEnabled,
		DailyCheckinCritProbability:     settings.DailyCheckinCritProbability,
		DailyCheckinCritMultiplier:      settings.DailyCheckinCritMultiplier,
		DailyCheckinCritMaxRewardUSD:    settings.DailyCheckinCritMaxRewardUSD,

		ModelPlazaEnabled:       settings.ModelPlazaEnabled,
		ModelPlazaRequireAuth:   settings.ModelPlazaRequireAuth,
		PluginManagementEnabled: settings.PluginManagementEnabled,
		ModelPlazaDescription:   settings.ModelPlazaDescription,

		AffiliateEnabled: settings.AffiliateEnabled,

		AccountSchedulingThresholds: settings.AccountSchedulingThresholds,
		AllowUserViewErrorRequests:  settings.AllowUserViewErrorRequests,
	}

	// OpenAI fast policy (stored under a dedicated setting key)
	if fastPolicy, err := h.settingService.GetOpenAIFastPolicySettings(c.Request.Context()); err != nil {
		slog.Error("openai_fast_policy_settings_get_failed", "error", err)
	} else if fastPolicy != nil {
		payload.OpenAIFastPolicySettings = openaiFastPolicySettingsToDTO(fastPolicy)
	}

	// Default platform quotas（JSON map）
	if platformQuotas, err := h.settingService.GetDefaultPlatformQuotas(c.Request.Context()); err != nil {
		slog.Error("default_platform_quotas_get_failed", "error", err)
	} else {
		payload.DefaultPlatformQuotas = platformQuotas
	}

	responseData := systemSettingsResponseData(payload, authSourceDefaults)
	if raw, err := h.settingService.GetValue(c.Request.Context(), service.SettingKeyOpenAIOAuthSchedulingRateMultiplier); err == nil && strings.TrimSpace(raw) == "" {
		responseData["openai_oauth_scheduling_rate_multiplier"] = nil
	}
	response.Success(c, responseData)
}

// GetDailyCheckinStats returns lightweight operational metrics for the check-in campaign.
// GET /api/v1/admin/settings/daily-checkin/stats
func (h *SettingHandler) GetDailyCheckinStats(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Daily check-in service is unavailable")
		return
	}
	userTZ, err := operationsTimezoneFromQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	stats, err := h.dailyCheckinService.GetAdminStats(c.Request.Context(), userTZ)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Keep the legacy settings alias response stable; the operations route is
	// the canonical endpoint that exposes reporting metadata.
	if strings.Contains(c.Request.URL.Path, "/admin/settings/") {
		stats.Meta = nil
	}
	response.Success(c, stats)
}

// GetOperationsOverview returns compact daily operations metrics.
// GET /api/v1/admin/operations/overview
func (h *SettingHandler) GetOperationsOverview(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Operations service is unavailable")
		return
	}
	start, end, err := parseOperationsDateRange(c, 30)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	data, err := h.dailyCheckinService.GetOperationsOverview(c.Request.Context(), start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetOperationsRetention returns registration-cohort API retention metrics.
// GET /api/v1/admin/operations/retention
func (h *SettingHandler) GetOperationsRetention(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Operations service is unavailable")
		return
	}
	start, end, err := parseOperationsDateRange(c, 30)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	data, err := h.dailyCheckinService.GetOperationsRetention(c.Request.Context(), start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDailyCheckinAnalytics returns check-in activity quality metrics.
// GET /api/v1/admin/operations/daily-checkin/analytics
func (h *SettingHandler) GetDailyCheckinAnalytics(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Daily check-in service is unavailable")
		return
	}
	start, end, err := parseOperationsDateRange(c, 30)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	data, err := h.dailyCheckinService.GetDailyCheckinAnalytics(c.Request.Context(), start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// ExportOperationsData exports operations datasets to CSV.
// GET /api/v1/admin/operations/export
func (h *SettingHandler) ExportOperationsData(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Operations service is unavailable")
		return
	}
	dataset := strings.TrimSpace(c.Query("dataset"))
	start, end, err := parseOperationsDateRange(c, 30)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if dataset == "daily_checkin_records" {
		h.streamDailyCheckinRecordsCSV(c, start, end)
		return
	}

	filename := fmt.Sprintf("operations_%s_%s_to_%s.csv", dataset, start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"))
	var buf bytes.Buffer
	if _, err := buf.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		response.InternalError(c, "Failed to export operations data: "+err.Error())
		return
	}
	writer := csv.NewWriter(&buf)

	switch dataset {
	case "overview_daily":
		err = h.writeOperationsOverviewCSV(c, writer, start, end)
	case "daily_checkin_summary":
		err = h.writeDailyCheckinSummaryCSV(c, writer, start, end)
	default:
		response.BadRequest(c, "Invalid dataset")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		response.InternalError(c, "Failed to export operations data: "+err.Error())
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

func (h *SettingHandler) streamDailyCheckinRecordsCSV(c *gin.Context, start, end time.Time) {
	filter := dailyCheckinRecordFilterFromQuery(c)
	applyDailyCheckinRecordExportDateDefaults(&filter, start, end)
	filename := fmt.Sprintf("operations_daily_checkin_records_%s_to_%s.csv", filter.DateFrom, filter.DateTo)
	iter, err := h.dailyCheckinService.OpenAdminRecordIterator(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	defer func() { _ = iter.Close() }()

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(c.Writer)
	if err := h.writeDailyCheckinRecordsCSV(writer, iter); err != nil {
		_ = c.Error(err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = c.Error(err)
	}
}

func (h *SettingHandler) writeOperationsOverviewCSV(c *gin.Context, writer *csv.Writer, start, end time.Time) error {
	data, err := h.dailyCheckinService.GetOperationsOverview(c.Request.Context(), start, end)
	if err != nil {
		return err
	}
	if err := writer.Write([]string{"date", "dau", "new_users", "request_users", "requests", "actual_cost"}); err != nil {
		return err
	}
	for _, point := range data.Points {
		if err := writer.Write([]string{
			point.Date,
			strconv.FormatInt(point.DAU, 10),
			strconv.FormatInt(point.NewUsers, 10),
			strconv.FormatInt(point.RequestUsers, 10),
			strconv.FormatInt(point.Requests, 10),
			formatCSVFloat(point.ActualCost),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *SettingHandler) writeDailyCheckinSummaryCSV(c *gin.Context, writer *csv.Writer, start, end time.Time) error {
	data, err := h.dailyCheckinService.GetDailyCheckinAnalytics(c.Request.Context(), start, end)
	if err != nil {
		return err
	}
	if err := writer.Write([]string{"date", "qualified_users", "checkin_users", "checkin_rate", "reward_usd", "avg_reward_usd", "fallback_count", "crit_count", "streak_user_count"}); err != nil {
		return err
	}
	for _, point := range data.Points {
		if err := writer.Write([]string{
			point.Date,
			strconv.FormatInt(point.QualifiedUsers, 10),
			strconv.FormatInt(point.CheckinUsers, 10),
			formatCSVFloat(point.CheckinRate),
			formatCSVFloat(point.RewardUSD),
			formatCSVFloat(point.AverageRewardUSD),
			strconv.FormatInt(point.FallbackCount, 10),
			strconv.FormatInt(point.CritCount, 10),
			strconv.FormatInt(point.StreakUserCount, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *SettingHandler) writeDailyCheckinRecordsCSV(writer *csv.Writer, iter *service.DailyCheckinAdminRecordIterator) error {
	if err := writer.Write([]string{"id", "user_id", "username", "email", "date", "reward_amount", "qualified_usage_usd", "base_reward_amount", "streak_days", "streak_multiplier", "crit_hit", "crit_multiplier", "budget_fallback", "created_at"}); err != nil {
		return err
	}
	for {
		record, err := iter.Next()
		if err != nil {
			return err
		}
		if record == nil {
			return nil
		}
		baseReward := record.RewardAmount
		streakDays := 0
		streakMultiplier := 1.0
		critHit := false
		critMultiplier := 1.0
		budgetFallback := false
		if record.RewardMetadata != nil {
			baseReward = record.RewardMetadata.BaseRewardAmount
			streakDays = record.RewardMetadata.StreakDays
			streakMultiplier = record.RewardMetadata.StreakMultiplier
			critHit = record.RewardMetadata.CritHit
			critMultiplier = record.RewardMetadata.CritMultiplier
			budgetFallback = record.RewardMetadata.BudgetFallback
		}
		if err := writer.Write([]string{
			strconv.FormatInt(record.ID, 10),
			strconv.FormatInt(record.UserID, 10),
			neutralizeCSVFormula(record.Username),
			neutralizeCSVFormula(record.Email),
			record.Date,
			formatCSVFloat(record.RewardAmount),
			formatCSVFloat(record.QualifiedUsageUSD),
			formatCSVFloat(baseReward),
			strconv.Itoa(streakDays),
			formatCSVFloat(streakMultiplier),
			strconv.FormatBool(critHit),
			formatCSVFloat(critMultiplier),
			strconv.FormatBool(budgetFallback),
			record.CreatedAt.Format("2006-01-02 15:04:05"),
		}); err != nil {
			return err
		}
	}
}

func dailyCheckinRecordFilterFromQuery(c *gin.Context) service.DailyCheckinAdminRecordFilter {
	dateFrom := strings.TrimSpace(c.Query("date_from"))
	if dateFrom == "" {
		dateFrom = strings.TrimSpace(c.Query("start_date"))
	}
	dateTo := strings.TrimSpace(c.Query("date_to"))
	if dateTo == "" {
		dateTo = strings.TrimSpace(c.Query("end_date"))
	}
	filter := service.DailyCheckinAdminRecordFilter{
		Page:      parsePositiveIntQuery(c, "page", 1),
		PageSize:  parsePositiveIntQuery(c, "page_size", 20),
		DateFrom:  dateFrom,
		DateTo:    dateTo,
		UserQuery: strings.TrimSpace(c.Query("user")),
	}
	if v, ok := parseOptionalFloatQuery(c, "reward_min"); ok {
		filter.RewardMin = &v
	}
	if v, ok := parseOptionalFloatQuery(c, "reward_max"); ok {
		filter.RewardMax = &v
	}
	if raw := strings.TrimSpace(c.Query("crit_hit")); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			filter.CritHit = &v
		}
	}
	if raw := strings.TrimSpace(c.Query("streak_days")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			filter.StreakDays = &v
		}
	}
	return filter
}

func applyDailyCheckinRecordExportDateDefaults(filter *service.DailyCheckinAdminRecordFilter, start, end time.Time) {
	if filter == nil {
		return
	}
	if strings.TrimSpace(filter.DateFrom) == "" {
		filter.DateFrom = start.Format("2006-01-02")
	}
	if strings.TrimSpace(filter.DateTo) == "" {
		filter.DateTo = end.AddDate(0, 0, -1).Format("2006-01-02")
	}
}

func parseOperationsDateRange(c *gin.Context, defaultDays int) (time.Time, time.Time, error) {
	if defaultDays <= 0 {
		defaultDays = 30
	}
	userTZ, err := operationsTimezoneFromQuery(c)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	now := timezone.NowInUserLocation(userTZ)
	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	var start, end time.Time
	if startDate == "" {
		start = timezone.StartOfDayInUserLocation(now.AddDate(0, 0, -defaultDays+1), userTZ)
	} else {
		start, err = timezone.ParseInUserLocation("2006-01-02", startDate, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start_date")
		}
	}
	if endDate == "" {
		end = timezone.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
	} else {
		parsed, err := timezone.ParseInUserLocation("2006-01-02", endDate, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end_date")
		}
		end = parsed.AddDate(0, 0, 1)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start_date must be before end_date")
	}
	startCalendar := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endCalendar := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	if endCalendar.Sub(startCalendar) > 370*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("date range is too large")
	}
	return start, end, nil
}

func operationsTimezoneFromQuery(c *gin.Context) (string, error) {
	userTZ := strings.TrimSpace(c.Query("timezone"))
	if userTZ == "" {
		return timezone.Name(), nil
	}
	if _, err := time.LoadLocation(userTZ); err != nil {
		return "", fmt.Errorf("invalid timezone")
	}
	return userTZ, nil
}

func formatCSVFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

// ListDailyCheckinRecords returns admin-facing check-in records for operations center.
// GET /api/v1/admin/operations/daily-checkin/records
func (h *SettingHandler) ListDailyCheckinRecords(c *gin.Context) {
	if h.dailyCheckinService == nil {
		response.InternalError(c, "Daily check-in service is unavailable")
		return
	}
	filter := dailyCheckinRecordFilterFromQuery(c)
	result, err := h.dailyCheckinService.ListAdminRecords(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// UpdateDailyCheckinSettings updates only daily check-in operation rules.
// PUT /api/v1/admin/operations/daily-checkin/settings
func (h *SettingHandler) UpdateDailyCheckinSettings(c *gin.Context) {
	var req UpdateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	previousSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	settings := service.DailyCheckinSettings{
		Enabled: func() bool {
			if req.DailyCheckinEnabled != nil {
				return *req.DailyCheckinEnabled
			}
			return previousSettings.DailyCheckinEnabled
		}(),
		RequiredUsageUSD: func() float64 {
			if req.DailyCheckinRequiredUsageUSD != nil {
				return *req.DailyCheckinRequiredUsageUSD
			}
			return previousSettings.DailyCheckinRequiredUsageUSD
		}(),
		UsageScope: func() string {
			if req.DailyCheckinUsageScope != nil {
				return strings.TrimSpace(*req.DailyCheckinUsageScope)
			}
			return previousSettings.DailyCheckinUsageScope
		}(),
		RewardMinUSD: func() float64 {
			if req.DailyCheckinRewardMinUSD != nil {
				return *req.DailyCheckinRewardMinUSD
			}
			return previousSettings.DailyCheckinRewardMinUSD
		}(),
		RewardMaxUSD: func() float64 {
			if req.DailyCheckinRewardMaxUSD != nil {
				return *req.DailyCheckinRewardMaxUSD
			}
			return previousSettings.DailyCheckinRewardMaxUSD
		}(),
		DailyBudgetUSD: func() float64 {
			if req.DailyCheckinDailyBudgetUSD != nil {
				return *req.DailyCheckinDailyBudgetUSD
			}
			return previousSettings.DailyCheckinDailyBudgetUSD
		}(),
		MonthlyBudgetUSD: func() float64 {
			if req.DailyCheckinMonthlyBudgetUSD != nil {
				return *req.DailyCheckinMonthlyBudgetUSD
			}
			return previousSettings.DailyCheckinMonthlyBudgetUSD
		}(),
		UserMonthlyLimitUSD: func() float64 {
			if req.DailyCheckinUserMonthlyLimitUSD != nil {
				return *req.DailyCheckinUserMonthlyLimitUSD
			}
			return previousSettings.DailyCheckinUserMonthlyLimitUSD
		}(),
		BudgetFallbackUSD: func() float64 {
			if req.DailyCheckinBudgetFallbackUSD != nil {
				return *req.DailyCheckinBudgetFallbackUSD
			}
			return previousSettings.DailyCheckinBudgetFallbackUSD
		}(),
		BudgetFallbackText: func() string {
			if req.DailyCheckinBudgetFallbackText != nil {
				return strings.TrimSpace(*req.DailyCheckinBudgetFallbackText)
			}
			return previousSettings.DailyCheckinBudgetFallbackText
		}(),
		RewardTiers: func() []service.DailyCheckinRewardTier {
			if req.DailyCheckinRewardTiers != nil {
				return *req.DailyCheckinRewardTiers
			}
			return previousSettings.DailyCheckinRewardTiers
		}(),
		StreakEnabled: func() bool {
			if req.DailyCheckinStreakEnabled != nil {
				return *req.DailyCheckinStreakEnabled
			}
			return previousSettings.DailyCheckinStreakEnabled
		}(),
		StreakScope: func() string {
			if req.DailyCheckinStreakScope != nil {
				return strings.TrimSpace(*req.DailyCheckinStreakScope)
			}
			return previousSettings.DailyCheckinStreakScope
		}(),
		StreakMultipliers: func() []service.DailyCheckinStreakMultiplier {
			if req.DailyCheckinStreakMultipliers != nil {
				return *req.DailyCheckinStreakMultipliers
			}
			return previousSettings.DailyCheckinStreakMultipliers
		}(),
		CritEnabled: func() bool {
			if req.DailyCheckinCritEnabled != nil {
				return *req.DailyCheckinCritEnabled
			}
			return previousSettings.DailyCheckinCritEnabled
		}(),
		CritProbability: func() float64 {
			if req.DailyCheckinCritProbability != nil {
				return *req.DailyCheckinCritProbability
			}
			return previousSettings.DailyCheckinCritProbability
		}(),
		CritMultiplier: func() float64 {
			if req.DailyCheckinCritMultiplier != nil {
				return *req.DailyCheckinCritMultiplier
			}
			return previousSettings.DailyCheckinCritMultiplier
		}(),
		CritMaxRewardUSD: func() float64 {
			if req.DailyCheckinCritMaxRewardUSD != nil {
				return *req.DailyCheckinCritMaxRewardUSD
			}
			return previousSettings.DailyCheckinCritMaxRewardUSD
		}(),
	}
	if err := h.settingService.UpdateDailyCheckinSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedSettings, err := h.settingService.GetAllSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.auditSettingsUpdate(c, previousSettings, updatedSettings, nil, nil, req)
	h.GetSettings(c)
}

func parsePositiveIntQuery(c *gin.Context, key string, fallback int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func parseOptionalFloatQuery(c *gin.Context, key string) (float64, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// openaiFastPolicySettingsToDTO converts service -> dto for OpenAI fast policy.
func openaiFastPolicySettingsToDTO(s *service.OpenAIFastPolicySettings) *dto.OpenAIFastPolicySettings {
	if s == nil {
		return nil
	}
	rules := make([]dto.OpenAIFastPolicyRule, len(s.Rules))
	for i, r := range s.Rules {
		rules[i] = dto.OpenAIFastPolicyRule(r)
	}
	return &dto.OpenAIFastPolicySettings{Rules: rules}
}

// openaiFastPolicySettingsFromDTO converts dto -> service for OpenAI fast policy.
//
// 规范化 ServiceTier：在 DTO 进入 service 层之前统一把空字符串归一为
// service.OpenAIFastTierAny ("all")，避免管理员保存时空串与 "all" 同时
// 表达"匹配任意 tier"造成数据库取值的二义性。其它非空值原样透传，由
// service.SetOpenAIFastPolicySettings 负责合法值校验。
func openaiFastPolicySettingsFromDTO(s *dto.OpenAIFastPolicySettings) *service.OpenAIFastPolicySettings {
	if s == nil {
		return nil
	}
	rules := make([]service.OpenAIFastPolicyRule, len(s.Rules))
	for i, r := range s.Rules {
		rules[i] = service.OpenAIFastPolicyRule(r)
		tier := strings.ToLower(strings.TrimSpace(rules[i].ServiceTier))
		if tier == "" {
			tier = service.OpenAIFastTierAny
		}
		rules[i].ServiceTier = tier
	}
	return &service.OpenAIFastPolicySettings{Rules: rules}
}

func loginAgreementDocumentsToDTO(items []service.LoginAgreementDocument) []dto.LoginAgreementDocument {
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

func loginAgreementDocumentsToService(items []dto.LoginAgreementDocument) []service.LoginAgreementDocument {
	result := make([]service.LoginAgreementDocument, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		content := strings.TrimSpace(item.ContentMD)
		if title == "" && content == "" {
			continue
		}
		result = append(result, service.LoginAgreementDocument{
			ID:        strings.TrimSpace(item.ID),
			Title:     title,
			ContentMD: content,
		})
	}
	return result
}

func (r *UpdateSettingsRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateSettingsRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = UpdateSettingsRequest(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, r.openAIOAuthSchedulingRateMultiplierSet = fields["openai_oauth_scheduling_rate_multiplier"]
	return nil
}

// hydrateOmittedSettingsRequest 用存量设置补齐请求中未出现的非指针字段，使后续
// 跨字段校验基于“存量值 + 本次补丁”的有效配置。omitted 仍负责阻止这些补充值
// 被持久化，因此显式提交零值的清空语义保持不变。
func hydrateOmittedSettingsRequest(
	req *UpdateSettingsRequest,
	previous *service.SystemSettings,
	sentFields map[string]json.RawMessage,
) {
	if req == nil || previous == nil {
		return
	}
	requestValue := reflect.ValueOf(req).Elem()
	previousValue := reflect.ValueOf(previous).Elem()
	requestType := requestValue.Type()
	for i := 0; i < requestType.NumField(); i++ {
		field := requestType.Field(i)
		if field.Type.Kind() == reflect.Pointer {
			continue
		}
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		if _, sent := sentFields[jsonName]; sent {
			continue
		}
		target := requestValue.Field(i)
		source := previousValue.FieldByName(field.Name)
		if !target.CanSet() || !source.IsValid() || !source.Type().AssignableTo(target.Type()) {
			continue
		}
		target.Set(source)
	}
}

func (h *SettingHandler) auditSettingsUpdate(c *gin.Context, before *service.SystemSettings, after *service.SystemSettings, beforeAuthSourceDefaults *service.AuthSourceDefaultSettings, afterAuthSourceDefaults *service.AuthSourceDefaultSettings, req UpdateSettingsRequest) {
	if before == nil || after == nil {
		return
	}

	changed := diffSettings(before, after, beforeAuthSourceDefaults, afterAuthSourceDefaults, req)
	if len(changed) == 0 {
		return
	}

	subject, _ := middleware.GetAuthSubjectFromContext(c)
	role, _ := middleware.GetUserRoleFromContext(c)
	slog.Info("settings updated",
		"audit", true,
		"user_id", subject.UserID,
		"role", role,
		"changed", changed,
	)
}

func diffSettings(before *service.SystemSettings, after *service.SystemSettings, beforeAuthSourceDefaults *service.AuthSourceDefaultSettings, afterAuthSourceDefaults *service.AuthSourceDefaultSettings, req UpdateSettingsRequest) []string {
	changed := make([]string, 0, 20)
	if before.RegistrationEnabled != after.RegistrationEnabled {
		changed = append(changed, "registration_enabled")
	}
	if before.CompactHomeEnabled != after.CompactHomeEnabled {
		changed = append(changed, "compact_home_enabled")
	}
	if before.EmailVerifyEnabled != after.EmailVerifyEnabled {
		changed = append(changed, "email_verify_enabled")
	}
	if !equalStringSlice(before.RegistrationEmailSuffixWhitelist, after.RegistrationEmailSuffixWhitelist) {
		changed = append(changed, "registration_email_suffix_whitelist")
	}
	if before.PromoCodeEnabled != after.PromoCodeEnabled {
		changed = append(changed, "promo_code_enabled")
	}
	if before.InvitationCodeEnabled != after.InvitationCodeEnabled {
		changed = append(changed, "invitation_code_enabled")
	}
	if before.PasswordResetEnabled != after.PasswordResetEnabled {
		changed = append(changed, "password_reset_enabled")
	}
	if before.FrontendURL != after.FrontendURL {
		changed = append(changed, "frontend_url")
	}
	if before.TotpEnabled != after.TotpEnabled {
		changed = append(changed, "totp_enabled")
	}
	if before.PasskeyEnabled != after.PasskeyEnabled {
		changed = append(changed, "passkey_enabled")
	}
	if before.LoginAgreementEnabled != after.LoginAgreementEnabled {
		changed = append(changed, "login_agreement_enabled")
	}
	if before.LoginAgreementMode != after.LoginAgreementMode {
		changed = append(changed, "login_agreement_mode")
	}
	if before.LoginAgreementUpdatedAt != after.LoginAgreementUpdatedAt {
		changed = append(changed, "login_agreement_updated_at")
	}
	if !equalLoginAgreementDocuments(before.LoginAgreementDocuments, after.LoginAgreementDocuments) {
		changed = append(changed, "login_agreement_documents")
	}
	if before.SMTPHost != after.SMTPHost {
		changed = append(changed, "smtp_host")
	}
	if before.SMTPPort != after.SMTPPort {
		changed = append(changed, "smtp_port")
	}
	if before.SMTPUsername != after.SMTPUsername {
		changed = append(changed, "smtp_username")
	}
	if req.SMTPPassword != "" {
		changed = append(changed, "smtp_password")
	}
	if before.SMTPFrom != after.SMTPFrom {
		changed = append(changed, "smtp_from_email")
	}
	if before.SMTPFromName != after.SMTPFromName {
		changed = append(changed, "smtp_from_name")
	}
	if before.SMTPUseTLS != after.SMTPUseTLS {
		changed = append(changed, "smtp_use_tls")
	}
	if before.TurnstileEnabled != after.TurnstileEnabled {
		changed = append(changed, "turnstile_enabled")
	}
	if before.TurnstileSiteKey != after.TurnstileSiteKey {
		changed = append(changed, "turnstile_site_key")
	}
	if req.TurnstileSecretKey != "" {
		changed = append(changed, "turnstile_secret_key")
	}
	if before.TencentCaptchaEnabled != after.TencentCaptchaEnabled {
		changed = append(changed, "tencent_captcha_enabled")
	}
	if before.TencentCaptchaAppID != after.TencentCaptchaAppID {
		changed = append(changed, "tencent_captcha_app_id")
	}
	if before.TencentCaptchaRegion != after.TencentCaptchaRegion {
		changed = append(changed, "tencent_captcha_region")
	}
	if req.TencentCaptchaAppSecretKey != "" {
		changed = append(changed, "tencent_captcha_app_secret_key")
	}
	if req.TencentCaptchaCloudSecretID != "" {
		changed = append(changed, "tencent_captcha_cloud_secret_id")
	}
	if req.TencentCaptchaCloudSecretKey != "" {
		changed = append(changed, "tencent_captcha_cloud_secret_key")
	}
	if before.AliyunCaptchaEnabled != after.AliyunCaptchaEnabled {
		changed = append(changed, "aliyun_captcha_enabled")
	}
	if before.AliyunCaptchaAccessKeyID != after.AliyunCaptchaAccessKeyID {
		changed = append(changed, "aliyun_captcha_access_key_id")
	}
	if req.AliyunCaptchaAccessKeySecret != "" {
		changed = append(changed, "aliyun_captcha_access_key_secret")
	}
	if before.AliyunCaptchaSceneID != after.AliyunCaptchaSceneID {
		changed = append(changed, "aliyun_captcha_scene_id")
	}
	if before.AliyunCaptchaPrefix != after.AliyunCaptchaPrefix {
		changed = append(changed, "aliyun_captcha_prefix")
	}
	if before.AliyunCaptchaRegion != after.AliyunCaptchaRegion {
		changed = append(changed, "aliyun_captcha_region")
	}
	if before.APIKeyACLTrustForwardedIP != after.APIKeyACLTrustForwardedIP {
		changed = append(changed, "api_key_acl_trust_forwarded_ip")
	}
	if !equalStringSlice(before.ForwardedClientIPHeaders, after.ForwardedClientIPHeaders) {
		changed = append(changed, "forwarded_client_ip_headers")
	}
	if before.LinuxDoConnectEnabled != after.LinuxDoConnectEnabled {
		changed = append(changed, "linuxdo_connect_enabled")
	}
	if before.LinuxDoConnectClientID != after.LinuxDoConnectClientID {
		changed = append(changed, "linuxdo_connect_client_id")
	}
	if req.LinuxDoConnectClientSecret != "" {
		changed = append(changed, "linuxdo_connect_client_secret")
	}
	if before.LinuxDoConnectRedirectURL != after.LinuxDoConnectRedirectURL {
		changed = append(changed, "linuxdo_connect_redirect_url")
	}
	if before.DingTalkConnectEnabled != after.DingTalkConnectEnabled {
		changed = append(changed, "dingtalk_connect_enabled")
	}
	if before.DingTalkConnectClientID != after.DingTalkConnectClientID {
		changed = append(changed, "dingtalk_connect_client_id")
	}
	if req.DingTalkConnectClientSecret != "" {
		changed = append(changed, "dingtalk_connect_client_secret")
	}
	if before.DingTalkConnectRedirectURL != after.DingTalkConnectRedirectURL {
		changed = append(changed, "dingtalk_connect_redirect_url")
	}
	if before.DingTalkConnectCorpRestrictionPolicy != after.DingTalkConnectCorpRestrictionPolicy {
		changed = append(changed, "dingtalk_connect_corp_restriction_policy")
	}
	if before.DingTalkConnectInternalCorpID != after.DingTalkConnectInternalCorpID {
		changed = append(changed, "dingtalk_connect_internal_corp_id")
	}
	if before.DingTalkConnectBypassRegistration != after.DingTalkConnectBypassRegistration {
		changed = append(changed, "dingtalk_connect_bypass_registration")
	}
	if before.DingTalkConnectSyncCorpEmail != after.DingTalkConnectSyncCorpEmail {
		changed = append(changed, "dingtalk_connect_sync_corp_email")
	}
	if before.DingTalkConnectSyncDisplayName != after.DingTalkConnectSyncDisplayName {
		changed = append(changed, "dingtalk_connect_sync_display_name")
	}
	if before.DingTalkConnectSyncDept != after.DingTalkConnectSyncDept {
		changed = append(changed, "dingtalk_connect_sync_dept")
	}
	if before.DingTalkConnectSyncCorpEmailAttrKey != after.DingTalkConnectSyncCorpEmailAttrKey {
		changed = append(changed, "dingtalk_connect_sync_corp_email_attr_key")
	}
	if before.DingTalkConnectSyncDisplayNameAttrKey != after.DingTalkConnectSyncDisplayNameAttrKey {
		changed = append(changed, "dingtalk_connect_sync_display_name_attr_key")
	}
	if before.DingTalkConnectSyncDeptAttrKey != after.DingTalkConnectSyncDeptAttrKey {
		changed = append(changed, "dingtalk_connect_sync_dept_attr_key")
	}
	if before.WeChatConnectEnabled != after.WeChatConnectEnabled {
		changed = append(changed, "wechat_connect_enabled")
	}
	if before.WeChatConnectAppID != after.WeChatConnectAppID {
		changed = append(changed, "wechat_connect_app_id")
	}
	if req.WeChatConnectAppSecret != "" {
		changed = append(changed, "wechat_connect_app_secret")
	}
	if before.WeChatConnectOpenAppID != after.WeChatConnectOpenAppID {
		changed = append(changed, "wechat_connect_open_app_id")
	}
	if req.WeChatConnectOpenAppSecret != "" {
		changed = append(changed, "wechat_connect_open_app_secret")
	}
	if before.WeChatConnectMPAppID != after.WeChatConnectMPAppID {
		changed = append(changed, "wechat_connect_mp_app_id")
	}
	if req.WeChatConnectMPAppSecret != "" {
		changed = append(changed, "wechat_connect_mp_app_secret")
	}
	if before.WeChatConnectMobileAppID != after.WeChatConnectMobileAppID {
		changed = append(changed, "wechat_connect_mobile_app_id")
	}
	if req.WeChatConnectMobileAppSecret != "" {
		changed = append(changed, "wechat_connect_mobile_app_secret")
	}
	if before.WeChatConnectOpenEnabled != after.WeChatConnectOpenEnabled {
		changed = append(changed, "wechat_connect_open_enabled")
	}
	if before.WeChatConnectMPEnabled != after.WeChatConnectMPEnabled {
		changed = append(changed, "wechat_connect_mp_enabled")
	}
	if before.WeChatConnectMobileEnabled != after.WeChatConnectMobileEnabled {
		changed = append(changed, "wechat_connect_mobile_enabled")
	}
	if before.WeChatConnectMode != after.WeChatConnectMode {
		changed = append(changed, "wechat_connect_mode")
	}
	if before.WeChatConnectScopes != after.WeChatConnectScopes {
		changed = append(changed, "wechat_connect_scopes")
	}
	if before.WeChatConnectRedirectURL != after.WeChatConnectRedirectURL {
		changed = append(changed, "wechat_connect_redirect_url")
	}
	if before.WeChatConnectFrontendRedirectURL != after.WeChatConnectFrontendRedirectURL {
		changed = append(changed, "wechat_connect_frontend_redirect_url")
	}
	if before.OIDCConnectEnabled != after.OIDCConnectEnabled {
		changed = append(changed, "oidc_connect_enabled")
	}
	if before.OIDCConnectProviderName != after.OIDCConnectProviderName {
		changed = append(changed, "oidc_connect_provider_name")
	}
	if before.OIDCConnectClientID != after.OIDCConnectClientID {
		changed = append(changed, "oidc_connect_client_id")
	}
	if req.OIDCConnectClientSecret != "" {
		changed = append(changed, "oidc_connect_client_secret")
	}
	if before.OIDCConnectIssuerURL != after.OIDCConnectIssuerURL {
		changed = append(changed, "oidc_connect_issuer_url")
	}
	if before.OIDCConnectDiscoveryURL != after.OIDCConnectDiscoveryURL {
		changed = append(changed, "oidc_connect_discovery_url")
	}
	if before.OIDCConnectAuthorizeURL != after.OIDCConnectAuthorizeURL {
		changed = append(changed, "oidc_connect_authorize_url")
	}
	if before.OIDCConnectTokenURL != after.OIDCConnectTokenURL {
		changed = append(changed, "oidc_connect_token_url")
	}
	if before.OIDCConnectUserInfoURL != after.OIDCConnectUserInfoURL {
		changed = append(changed, "oidc_connect_userinfo_url")
	}
	if before.OIDCConnectJWKSURL != after.OIDCConnectJWKSURL {
		changed = append(changed, "oidc_connect_jwks_url")
	}
	if before.OIDCConnectScopes != after.OIDCConnectScopes {
		changed = append(changed, "oidc_connect_scopes")
	}
	if before.OIDCConnectRedirectURL != after.OIDCConnectRedirectURL {
		changed = append(changed, "oidc_connect_redirect_url")
	}
	if before.OIDCConnectFrontendRedirectURL != after.OIDCConnectFrontendRedirectURL {
		changed = append(changed, "oidc_connect_frontend_redirect_url")
	}
	if before.OIDCConnectTokenAuthMethod != after.OIDCConnectTokenAuthMethod {
		changed = append(changed, "oidc_connect_token_auth_method")
	}
	if before.OIDCConnectUsePKCE != after.OIDCConnectUsePKCE {
		changed = append(changed, "oidc_connect_use_pkce")
	}
	if before.OIDCConnectValidateIDToken != after.OIDCConnectValidateIDToken {
		changed = append(changed, "oidc_connect_validate_id_token")
	}
	if before.OIDCConnectAllowedSigningAlgs != after.OIDCConnectAllowedSigningAlgs {
		changed = append(changed, "oidc_connect_allowed_signing_algs")
	}
	if before.OIDCConnectClockSkewSeconds != after.OIDCConnectClockSkewSeconds {
		changed = append(changed, "oidc_connect_clock_skew_seconds")
	}
	if before.OIDCConnectRequireEmailVerified != after.OIDCConnectRequireEmailVerified {
		changed = append(changed, "oidc_connect_require_email_verified")
	}
	if before.OIDCConnectUserInfoEmailPath != after.OIDCConnectUserInfoEmailPath {
		changed = append(changed, "oidc_connect_userinfo_email_path")
	}
	if before.OIDCConnectUserInfoIDPath != after.OIDCConnectUserInfoIDPath {
		changed = append(changed, "oidc_connect_userinfo_id_path")
	}
	if before.OIDCConnectUserInfoUsernamePath != after.OIDCConnectUserInfoUsernamePath {
		changed = append(changed, "oidc_connect_userinfo_username_path")
	}
	if before.SiteName != after.SiteName {
		changed = append(changed, "site_name")
	}
	if before.SiteLogo != after.SiteLogo {
		changed = append(changed, "site_logo")
	}
	if before.SiteSubtitle != after.SiteSubtitle {
		changed = append(changed, "site_subtitle")
	}
	if before.APIBaseURL != after.APIBaseURL {
		changed = append(changed, "api_base_url")
	}
	if before.ContactInfo != after.ContactInfo {
		changed = append(changed, "contact_info")
	}
	if before.DocURL != after.DocURL {
		changed = append(changed, "doc_url")
	}
	if before.HomeContent != after.HomeContent {
		changed = append(changed, "home_content")
	}
	if before.SiteLaunchAt != after.SiteLaunchAt {
		changed = append(changed, "site_launch_at")
	}
	if before.HideCcsImportButton != after.HideCcsImportButton {
		changed = append(changed, "hide_ccs_import_button")
	}
	if before.DefaultConcurrency != after.DefaultConcurrency {
		changed = append(changed, "default_concurrency")
	}
	if before.DefaultBalance != after.DefaultBalance {
		changed = append(changed, "default_balance")
	}
	if before.AffiliateRebateRate != after.AffiliateRebateRate {
		changed = append(changed, "affiliate_rebate_rate")
	}
	if before.AffiliateRebateFreezeHours != after.AffiliateRebateFreezeHours {
		changed = append(changed, "affiliate_rebate_freeze_hours")
	}
	if before.AffiliateRebateDurationDays != after.AffiliateRebateDurationDays {
		changed = append(changed, "affiliate_rebate_duration_days")
	}
	if before.AffiliateRebatePerInviteeCap != after.AffiliateRebatePerInviteeCap {
		changed = append(changed, "affiliate_rebate_per_invitee_cap")
	}
	if !equalDefaultSubscriptions(before.DefaultSubscriptions, after.DefaultSubscriptions) {
		changed = append(changed, "default_subscriptions")
	}
	if before.EnableModelFallback != after.EnableModelFallback {
		changed = append(changed, "enable_model_fallback")
	}
	if before.FallbackModelAnthropic != after.FallbackModelAnthropic {
		changed = append(changed, "fallback_model_anthropic")
	}
	if before.FallbackModelOpenAI != after.FallbackModelOpenAI {
		changed = append(changed, "fallback_model_openai")
	}
	if before.FallbackModelGemini != after.FallbackModelGemini {
		changed = append(changed, "fallback_model_gemini")
	}
	if before.FallbackModelAntigravity != after.FallbackModelAntigravity {
		changed = append(changed, "fallback_model_antigravity")
	}
	if before.EnableIdentityPatch != after.EnableIdentityPatch {
		changed = append(changed, "enable_identity_patch")
	}
	if before.IdentityPatchPrompt != after.IdentityPatchPrompt {
		changed = append(changed, "identity_patch_prompt")
	}
	if before.OpsMonitoringEnabled != after.OpsMonitoringEnabled {
		changed = append(changed, "ops_monitoring_enabled")
	}
	if before.OpsRealtimeMonitoringEnabled != after.OpsRealtimeMonitoringEnabled {
		changed = append(changed, "ops_realtime_monitoring_enabled")
	}
	if before.OpsQueryModeDefault != after.OpsQueryModeDefault {
		changed = append(changed, "ops_query_mode_default")
	}
	if before.OpsMetricsIntervalSeconds != after.OpsMetricsIntervalSeconds {
		changed = append(changed, "ops_metrics_interval_seconds")
	}
	if before.MinClaudeCodeVersion != after.MinClaudeCodeVersion {
		changed = append(changed, "min_claude_code_version")
	}
	if before.MaxClaudeCodeVersion != after.MaxClaudeCodeVersion {
		changed = append(changed, "max_claude_code_version")
	}
	if before.AllowUngroupedKeyScheduling != after.AllowUngroupedKeyScheduling {
		changed = append(changed, "allow_ungrouped_key_scheduling")
	}
	if before.BackendModeEnabled != after.BackendModeEnabled {
		changed = append(changed, "backend_mode_enabled")
	}
	if before.PurchaseSubscriptionEnabled != after.PurchaseSubscriptionEnabled {
		changed = append(changed, "purchase_subscription_enabled")
	}
	if before.PurchaseSubscriptionURL != after.PurchaseSubscriptionURL {
		changed = append(changed, "purchase_subscription_url")
	}
	if before.TableDefaultPageSize != after.TableDefaultPageSize {
		changed = append(changed, "table_default_page_size")
	}
	if !equalIntSlice(before.TablePageSizeOptions, after.TablePageSizeOptions) {
		changed = append(changed, "table_page_size_options")
	}
	if before.CustomMenuItems != after.CustomMenuItems {
		changed = append(changed, "custom_menu_items")
	}
	if before.CustomEndpoints != after.CustomEndpoints {
		changed = append(changed, "custom_endpoints")
	}
	if before.ClarityEnabled != after.ClarityEnabled {
		changed = append(changed, "clarity_enabled")
	}
	if before.ClarityProjectID != after.ClarityProjectID {
		changed = append(changed, "clarity_project_id")
	}
	if before.EnableFingerprintUnification != after.EnableFingerprintUnification {
		changed = append(changed, "enable_fingerprint_unification")
	}
	if before.EnableMetadataPassthrough != after.EnableMetadataPassthrough {
		changed = append(changed, "enable_metadata_passthrough")
	}
	if before.EnableCCHSigning != after.EnableCCHSigning {
		changed = append(changed, "enable_cch_signing")
	}
	if before.EnableClaudeOAuthSystemPromptInjection != after.EnableClaudeOAuthSystemPromptInjection {
		changed = append(changed, "enable_claude_oauth_system_prompt_injection")
	}
	if before.ClaudeOAuthSystemPrompt != after.ClaudeOAuthSystemPrompt {
		changed = append(changed, "claude_oauth_system_prompt")
	}
	if before.ClaudeOAuthSystemPromptBlocks != after.ClaudeOAuthSystemPromptBlocks {
		changed = append(changed, "claude_oauth_system_prompt_blocks")
	}
	if before.EnableAnthropicCacheTTL1hInjection != after.EnableAnthropicCacheTTL1hInjection {
		changed = append(changed, "enable_anthropic_cache_ttl_1h_injection")
	}
	if before.RewriteMessageCacheControl != after.RewriteMessageCacheControl {
		changed = append(changed, "rewrite_message_cache_control")
	}
	if before.EnableClientDatelineNormalization != after.EnableClientDatelineNormalization {
		changed = append(changed, "enable_client_dateline_normalization")
	}
	if before.AntigravityUserAgentVersion != after.AntigravityUserAgentVersion {
		changed = append(changed, "antigravity_user_agent_version")
	}
	if before.OpenAICodexUserAgent != after.OpenAICodexUserAgent {
		changed = append(changed, "openai_codex_user_agent")
	}
	if before.OpenAICodexClientVersion != after.OpenAICodexClientVersion {
		changed = append(changed, "openai_codex_client_version")
	}
	if before.OpenAICodexVersionAutoSyncEnabled != after.OpenAICodexVersionAutoSyncEnabled {
		changed = append(changed, "openai_codex_version_auto_sync_enabled")
	}
	if before.MinCodexVersion != after.MinCodexVersion {
		changed = append(changed, "min_codex_version")
	}
	if before.MaxCodexVersion != after.MaxCodexVersion {
		changed = append(changed, "max_codex_version")
	}
	if before.CodexCLIOnlyBlacklist != after.CodexCLIOnlyBlacklist {
		changed = append(changed, "codex_cli_only_blacklist")
	}
	if before.CodexCLIOnlyWhitelist != after.CodexCLIOnlyWhitelist {
		changed = append(changed, "codex_cli_only_whitelist")
	}
	if before.CodexCLIOnlyAllowAppServerClients != after.CodexCLIOnlyAllowAppServerClients {
		changed = append(changed, "codex_cli_only_allow_app_server_clients")
	}
	if before.CodexCLIOnlyEngineFingerprintSignals != after.CodexCLIOnlyEngineFingerprintSignals {
		changed = append(changed, "codex_cli_only_engine_fingerprint_signals")
	}
	if before.PaymentVisibleMethodAlipaySource != after.PaymentVisibleMethodAlipaySource {
		changed = append(changed, "payment_visible_method_alipay_source")
	}
	if before.PaymentVisibleMethodWxpaySource != after.PaymentVisibleMethodWxpaySource {
		changed = append(changed, "payment_visible_method_wxpay_source")
	}
	if before.PaymentVisibleMethodAlipayEnabled != after.PaymentVisibleMethodAlipayEnabled {
		changed = append(changed, "payment_visible_method_alipay_enabled")
	}
	if before.PaymentVisibleMethodWxpayEnabled != after.PaymentVisibleMethodWxpayEnabled {
		changed = append(changed, "payment_visible_method_wxpay_enabled")
	}
	if before.AccountQualitySchedulingEnabled != after.AccountQualitySchedulingEnabled {
		changed = append(changed, "account_quality_scheduling_enabled")
	}
	if before.OpenAIAdvancedSchedulerEnabled != after.OpenAIAdvancedSchedulerEnabled {
		changed = append(changed, "openai_advanced_scheduler_enabled")
	}
	if before.OpenAILowUpstreamRatePriorityEnabled != after.OpenAILowUpstreamRatePriorityEnabled {
		changed = append(changed, "openai_low_upstream_rate_priority_enabled")
	}
	if before.OpenAIOAuthSchedulingRateMultiplier != after.OpenAIOAuthSchedulingRateMultiplier {
		changed = append(changed, "openai_oauth_scheduling_rate_multiplier")
	}
	if before.OpenAISchedulingUSDToCNYRate != after.OpenAISchedulingUSDToCNYRate {
		changed = append(changed, "openai_scheduling_usd_to_cny_rate")
	}

	if before.OpenAIAdvancedSchedulerStickyWeightedEnabled != after.OpenAIAdvancedSchedulerStickyWeightedEnabled {
		changed = append(changed, "openai_advanced_scheduler_sticky_weighted_enabled")
	}
	if before.OpenAIAdvancedSchedulerSubscriptionPriorityEnabled != after.OpenAIAdvancedSchedulerSubscriptionPriorityEnabled {
		changed = append(changed, "openai_advanced_scheduler_subscription_priority_enabled")
	}
	if before.OpenAIAdvancedSchedulerLBTopK != after.OpenAIAdvancedSchedulerLBTopK {
		changed = append(changed, "openai_advanced_scheduler_lb_top_k")
	}
	if before.OpenAIAdvancedSchedulerWeightPriority != after.OpenAIAdvancedSchedulerWeightPriority {
		changed = append(changed, "openai_advanced_scheduler_weight_priority")
	}
	if before.OpenAIAdvancedSchedulerWeightLoad != after.OpenAIAdvancedSchedulerWeightLoad {
		changed = append(changed, "openai_advanced_scheduler_weight_load")
	}
	if before.OpenAIAdvancedSchedulerWeightQueue != after.OpenAIAdvancedSchedulerWeightQueue {
		changed = append(changed, "openai_advanced_scheduler_weight_queue")
	}
	if before.OpenAIAdvancedSchedulerWeightErrorRate != after.OpenAIAdvancedSchedulerWeightErrorRate {
		changed = append(changed, "openai_advanced_scheduler_weight_error_rate")
	}
	if before.OpenAIAdvancedSchedulerWeightTTFT != after.OpenAIAdvancedSchedulerWeightTTFT {
		changed = append(changed, "openai_advanced_scheduler_weight_ttft")
	}
	if before.OpenAIAdvancedSchedulerWeightReset != after.OpenAIAdvancedSchedulerWeightReset {
		changed = append(changed, "openai_advanced_scheduler_weight_reset")
	}
	if before.OpenAIAdvancedSchedulerWeightQuotaHeadroom != after.OpenAIAdvancedSchedulerWeightQuotaHeadroom {
		changed = append(changed, "openai_advanced_scheduler_weight_quota_headroom")
	}
	if before.OpenAIAdvancedSchedulerWeightUpstreamCost != after.OpenAIAdvancedSchedulerWeightUpstreamCost {
		changed = append(changed, "openai_advanced_scheduler_weight_upstream_cost")
	}
	if before.OpenAIAdvancedSchedulerWeightPreviousResponse != after.OpenAIAdvancedSchedulerWeightPreviousResponse {
		changed = append(changed, "openai_advanced_scheduler_weight_previous_response")
	}
	if before.OpenAIAdvancedSchedulerWeightSessionSticky != after.OpenAIAdvancedSchedulerWeightSessionSticky {
		changed = append(changed, "openai_advanced_scheduler_weight_session_sticky")

	}
	// 余额、订阅到期与账号限额通知
	if before.BalanceLowNotifyEnabled != after.BalanceLowNotifyEnabled {
		changed = append(changed, "balance_low_notify_enabled")
	}
	if before.BalanceLowNotifyThreshold != after.BalanceLowNotifyThreshold {
		changed = append(changed, "balance_low_notify_threshold")
	}
	if before.BalanceLowNotifyRechargeURL != after.BalanceLowNotifyRechargeURL {
		changed = append(changed, "balance_low_notify_recharge_url")
	}
	if before.SubscriptionExpiryNotifyEnabled != after.SubscriptionExpiryNotifyEnabled {
		changed = append(changed, "subscription_expiry_notify_enabled")
	}
	if before.AccountQuotaNotifyEnabled != after.AccountQuotaNotifyEnabled {
		changed = append(changed, "account_quota_notify_enabled")
	}
	if !equalNotifyEmailEntries(before.AccountQuotaNotifyEmails, after.AccountQuotaNotifyEmails) {
		changed = append(changed, "account_quota_notify_emails")
	}
	if before.ChannelMonitorEnabled != after.ChannelMonitorEnabled {
		changed = append(changed, "channel_monitor_enabled")
	}
	if before.ChannelMonitorDefaultIntervalSeconds != after.ChannelMonitorDefaultIntervalSeconds {
		changed = append(changed, "channel_monitor_default_interval_seconds")
	}
	if before.ChannelMonitorMode != after.ChannelMonitorMode {
		changed = append(changed, "channel_monitor_mode")
	}
	if before.ChannelMonitorHideThroughput != after.ChannelMonitorHideThroughput {
		changed = append(changed, "channel_monitor_hide_throughput")
	}
	if before.ChannelMonitorHideUserRanking != after.ChannelMonitorHideUserRanking {
		changed = append(changed, "channel_monitor_hide_user_ranking")
	}
	if before.GrokDefaultTextModel != after.GrokDefaultTextModel {
		changed = append(changed, "grok_default_text_model")
	}
	if before.GrokCrossClientModelMapEnabled != after.GrokCrossClientModelMapEnabled {
		changed = append(changed, "grok_cross_client_model_map_enabled")
	}
	if before.GrokDefaultBaseURLMode != after.GrokDefaultBaseURLMode {
		changed = append(changed, "grok_default_base_url_mode")
	}
	if before.AvailableChannelsEnabled != after.AvailableChannelsEnabled {
		changed = append(changed, "available_channels_enabled")
	}
	if before.SubscriptionEnabled != after.SubscriptionEnabled {
		changed = append(changed, "subscription_enabled")
	}
	if before.WebConsoleEnabled != after.WebConsoleEnabled {
		changed = append(changed, "web_console_enabled")
	}
	if before.WebConsoleDefaultEndpoint != after.WebConsoleDefaultEndpoint {
		changed = append(changed, "web_console_default_endpoint")
	}
	if before.ModelPlazaEnabled != after.ModelPlazaEnabled {
		changed = append(changed, "model_plaza_enabled")
	}
	if before.PluginManagementEnabled != after.PluginManagementEnabled {
		changed = append(changed, "plugin_management_enabled")
	}
	if before.ModelPlazaRequireAuth != after.ModelPlazaRequireAuth {
		changed = append(changed, "model_plaza_require_auth")
	}
	if before.ModelPlazaDescription != after.ModelPlazaDescription {
		changed = append(changed, "model_plaza_description")
	}
	if before.DailyCheckinEnabled != after.DailyCheckinEnabled {
		changed = append(changed, "daily_checkin_enabled")
	}
	if before.DailyCheckinRequiredUsageUSD != after.DailyCheckinRequiredUsageUSD {
		changed = append(changed, "daily_checkin_required_usage_usd")
	}
	if before.DailyCheckinUsageScope != after.DailyCheckinUsageScope {
		changed = append(changed, "daily_checkin_usage_scope")
	}
	if before.DailyCheckinRewardMinUSD != after.DailyCheckinRewardMinUSD {
		changed = append(changed, "daily_checkin_reward_min_usd")
	}
	if before.DailyCheckinRewardMaxUSD != after.DailyCheckinRewardMaxUSD {
		changed = append(changed, "daily_checkin_reward_max_usd")
	}
	if before.DailyCheckinDailyBudgetUSD != after.DailyCheckinDailyBudgetUSD {
		changed = append(changed, "daily_checkin_daily_budget_usd")
	}
	if before.DailyCheckinMonthlyBudgetUSD != after.DailyCheckinMonthlyBudgetUSD {
		changed = append(changed, "daily_checkin_monthly_budget_usd")
	}
	if before.DailyCheckinUserMonthlyLimitUSD != after.DailyCheckinUserMonthlyLimitUSD {
		changed = append(changed, "daily_checkin_user_monthly_limit_usd")
	}
	if before.DailyCheckinBudgetFallbackUSD != after.DailyCheckinBudgetFallbackUSD {
		changed = append(changed, "daily_checkin_budget_fallback_reward_usd")
	}
	if before.DailyCheckinBudgetFallbackText != after.DailyCheckinBudgetFallbackText {
		changed = append(changed, "daily_checkin_budget_fallback_message")
	}
	if !equalJSONComparable(before.DailyCheckinRewardTiers, after.DailyCheckinRewardTiers) {
		changed = append(changed, "daily_checkin_reward_tiers")
	}
	if before.DailyCheckinStreakEnabled != after.DailyCheckinStreakEnabled {
		changed = append(changed, "daily_checkin_streak_multiplier_enabled")
	}
	if before.DailyCheckinStreakScope != after.DailyCheckinStreakScope {
		changed = append(changed, "daily_checkin_streak_multiplier_scope")
	}
	if !equalJSONComparable(before.DailyCheckinStreakMultipliers, after.DailyCheckinStreakMultipliers) {
		changed = append(changed, "daily_checkin_streak_multipliers")
	}
	if before.DailyCheckinCritEnabled != after.DailyCheckinCritEnabled {
		changed = append(changed, "daily_checkin_crit_enabled")
	}
	if before.DailyCheckinCritProbability != after.DailyCheckinCritProbability {
		changed = append(changed, "daily_checkin_crit_probability_percent")
	}
	if before.DailyCheckinCritMultiplier != after.DailyCheckinCritMultiplier {
		changed = append(changed, "daily_checkin_crit_multiplier")
	}
	if before.DailyCheckinCritMaxRewardUSD != after.DailyCheckinCritMaxRewardUSD {
		changed = append(changed, "daily_checkin_crit_max_reward_usd")
	}
	if before.AffiliateEnabled != after.AffiliateEnabled {
		changed = append(changed, "affiliate_enabled")
	}
	if before.RiskControlEnabled != after.RiskControlEnabled {
		changed = append(changed, "risk_control_enabled")
	}
	if before.CyberSessionBlockEnabled != after.CyberSessionBlockEnabled {
		changed = append(changed, "cyber_session_block_enabled")
	}
	if before.CyberSessionBlockTTLSeconds != after.CyberSessionBlockTTLSeconds {
		changed = append(changed, "cyber_session_block_ttl_seconds")
	}
	// Default platform quotas（JSON map，整体比较）
	if !equalPlatformQuotaSettings(before.DefaultPlatformQuotas, after.DefaultPlatformQuotas) {
		changed = append(changed, service.SettingKeyDefaultPlatformQuotas)
	}
	changed = appendAuthSourceDefaultChanges(changed, beforeAuthSourceDefaults, afterAuthSourceDefaults)
	return changed
}

func appendAuthSourceDefaultChanges(changed []string, before *service.AuthSourceDefaultSettings, after *service.AuthSourceDefaultSettings) []string {
	if before == nil {
		before = &service.AuthSourceDefaultSettings{}
	}
	if after == nil {
		after = &service.AuthSourceDefaultSettings{}
	}

	type providerDefaultGrantField struct {
		name   string
		before service.ProviderDefaultGrantSettings
		after  service.ProviderDefaultGrantSettings
	}

	fields := []providerDefaultGrantField{
		{name: "email", before: before.Email, after: after.Email},
		{name: "linuxdo", before: before.LinuxDo, after: after.LinuxDo},
		{name: "oidc", before: before.OIDC, after: after.OIDC},
		{name: "wechat", before: before.WeChat, after: after.WeChat},
		{name: "github", before: before.GitHub, after: after.GitHub},
		{name: "google", before: before.Google, after: after.Google},
		{name: "dingtalk", before: before.DingTalk, after: after.DingTalk},
	}
	for _, field := range fields {
		if field.before.Balance != field.after.Balance {
			changed = append(changed, "auth_source_default_"+field.name+"_balance")
		}
		if field.before.Concurrency != field.after.Concurrency {
			changed = append(changed, "auth_source_default_"+field.name+"_concurrency")
		}
		if !equalDefaultSubscriptions(field.before.Subscriptions, field.after.Subscriptions) {
			changed = append(changed, "auth_source_default_"+field.name+"_subscriptions")
		}
		if field.before.GrantOnSignup != field.after.GrantOnSignup {
			changed = append(changed, "auth_source_default_"+field.name+"_grant_on_signup")
		}
		if field.before.GrantOnFirstBind != field.after.GrantOnFirstBind {
			changed = append(changed, "auth_source_default_"+field.name+"_grant_on_first_bind")
		}
		// Platform quotas diff：整体替换语义，发单个 JSON key。
		if !equalPlatformQuotaSettings(field.before.PlatformQuotas, field.after.PlatformQuotas) {
			changed = append(changed, service.SettingKeyAuthSourcePlatformQuotas(field.name))
		}
	}
	if before.ForceEmailOnThirdPartySignup != after.ForceEmailOnThirdPartySignup {
		changed = append(changed, "force_email_on_third_party_signup")
	}
	return changed
}

func normalizeDefaultSubscriptions(input []dto.DefaultSubscriptionSetting) []dto.DefaultSubscriptionSetting {
	if len(input) == 0 {
		return nil
	}
	normalized := make([]dto.DefaultSubscriptionSetting, 0, len(input))
	for _, item := range input {
		if item.GroupID <= 0 || item.ValidityDays <= 0 {
			continue
		}
		if item.ValidityDays > service.MaxValidityDays {
			item.ValidityDays = service.MaxValidityDays
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func normalizeOptionalDefaultSubscriptions(input *[]dto.DefaultSubscriptionSetting) *[]dto.DefaultSubscriptionSetting {
	if input == nil {
		return nil
	}
	normalized := normalizeDefaultSubscriptions(*input)
	return &normalized
}

func float64ValueOrDefault(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func intValueOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func boolValueOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func defaultSubscriptionsValueOrDefault(input *[]dto.DefaultSubscriptionSetting, fallback []service.DefaultSubscriptionSetting) []service.DefaultSubscriptionSetting {
	if input == nil {
		return fallback
	}
	result := make([]service.DefaultSubscriptionSetting, 0, len(*input))
	for _, item := range *input {
		result = append(result, service.DefaultSubscriptionSetting{
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
		})
	}
	return result
}

// platformQuotasValueOrDefault 处理 auth-source platform quota 的 nil 语义：
// nil = 请求未包含该字段（保留 fallback），non-nil（含 empty map）= 整体覆盖。
// 注意：JSON null 与字段省略等价——两者均反序列化为 nil map，因此都保留旧值；
// 若要清空某 source 的所有 quota 配置，须显式发空对象 {}。
func platformQuotasValueOrDefault(value, fallback map[string]*service.DefaultPlatformQuotaSetting) map[string]*service.DefaultPlatformQuotaSetting {
	if value == nil {
		return fallback
	}
	return value
}

func systemSettingsResponseData(settings dto.SystemSettings, authSourceDefaults *service.AuthSourceDefaultSettings) map[string]any {
	data := make(map[string]any)
	raw, err := json.Marshal(settings)
	if err == nil {
		_ = json.Unmarshal(raw, &data)
	}
	if authSourceDefaults == nil {
		authSourceDefaults = &service.AuthSourceDefaultSettings{}
	}

	data["auth_source_default_email_balance"] = authSourceDefaults.Email.Balance
	data["auth_source_default_email_concurrency"] = authSourceDefaults.Email.Concurrency
	data["auth_source_default_email_subscriptions"] = authSourceDefaults.Email.Subscriptions
	data["auth_source_default_email_grant_on_signup"] = authSourceDefaults.Email.GrantOnSignup
	data["auth_source_default_email_grant_on_first_bind"] = authSourceDefaults.Email.GrantOnFirstBind
	data["auth_source_default_linuxdo_balance"] = authSourceDefaults.LinuxDo.Balance
	data["auth_source_default_linuxdo_concurrency"] = authSourceDefaults.LinuxDo.Concurrency
	data["auth_source_default_linuxdo_subscriptions"] = authSourceDefaults.LinuxDo.Subscriptions
	data["auth_source_default_linuxdo_grant_on_signup"] = authSourceDefaults.LinuxDo.GrantOnSignup
	data["auth_source_default_linuxdo_grant_on_first_bind"] = authSourceDefaults.LinuxDo.GrantOnFirstBind
	data["auth_source_default_dingtalk_balance"] = authSourceDefaults.DingTalk.Balance
	data["auth_source_default_dingtalk_concurrency"] = authSourceDefaults.DingTalk.Concurrency
	data["auth_source_default_dingtalk_subscriptions"] = authSourceDefaults.DingTalk.Subscriptions
	data["auth_source_default_dingtalk_grant_on_signup"] = authSourceDefaults.DingTalk.GrantOnSignup
	data["auth_source_default_dingtalk_grant_on_first_bind"] = authSourceDefaults.DingTalk.GrantOnFirstBind
	data["auth_source_default_oidc_balance"] = authSourceDefaults.OIDC.Balance
	data["auth_source_default_oidc_concurrency"] = authSourceDefaults.OIDC.Concurrency
	data["auth_source_default_oidc_subscriptions"] = authSourceDefaults.OIDC.Subscriptions
	data["auth_source_default_oidc_grant_on_signup"] = authSourceDefaults.OIDC.GrantOnSignup
	data["auth_source_default_oidc_grant_on_first_bind"] = authSourceDefaults.OIDC.GrantOnFirstBind
	data["auth_source_default_wechat_balance"] = authSourceDefaults.WeChat.Balance
	data["auth_source_default_wechat_concurrency"] = authSourceDefaults.WeChat.Concurrency
	data["auth_source_default_wechat_subscriptions"] = authSourceDefaults.WeChat.Subscriptions
	data["auth_source_default_wechat_grant_on_signup"] = authSourceDefaults.WeChat.GrantOnSignup
	data["auth_source_default_wechat_grant_on_first_bind"] = authSourceDefaults.WeChat.GrantOnFirstBind
	data["auth_source_default_github_balance"] = authSourceDefaults.GitHub.Balance
	data["auth_source_default_github_concurrency"] = authSourceDefaults.GitHub.Concurrency
	data["auth_source_default_github_subscriptions"] = authSourceDefaults.GitHub.Subscriptions
	data["auth_source_default_github_grant_on_signup"] = authSourceDefaults.GitHub.GrantOnSignup
	data["auth_source_default_github_grant_on_first_bind"] = authSourceDefaults.GitHub.GrantOnFirstBind
	data["auth_source_default_google_balance"] = authSourceDefaults.Google.Balance
	data["auth_source_default_google_concurrency"] = authSourceDefaults.Google.Concurrency
	data["auth_source_default_google_subscriptions"] = authSourceDefaults.Google.Subscriptions
	data["auth_source_default_google_grant_on_signup"] = authSourceDefaults.Google.GrantOnSignup
	data["auth_source_default_google_grant_on_first_bind"] = authSourceDefaults.Google.GrantOnFirstBind
	data["auth_source_default_email_platform_quotas"] = authSourceDefaults.Email.PlatformQuotas
	data["auth_source_default_linuxdo_platform_quotas"] = authSourceDefaults.LinuxDo.PlatformQuotas
	data["auth_source_default_oidc_platform_quotas"] = authSourceDefaults.OIDC.PlatformQuotas
	data["auth_source_default_wechat_platform_quotas"] = authSourceDefaults.WeChat.PlatformQuotas
	data["auth_source_default_github_platform_quotas"] = authSourceDefaults.GitHub.PlatformQuotas
	data["auth_source_default_google_platform_quotas"] = authSourceDefaults.Google.PlatformQuotas
	data["auth_source_default_dingtalk_platform_quotas"] = authSourceDefaults.DingTalk.PlatformQuotas
	data["force_email_on_third_party_signup"] = authSourceDefaults.ForceEmailOnThirdPartySignup

	return data
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalDefaultSubscriptions(a, b []service.DefaultSubscriptionSetting) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].GroupID != b[i].GroupID || a[i].ValidityDays != b[i].ValidityDays {
			return false
		}
	}
	return true
}

func equalLoginAgreementDocuments(a, b []service.LoginAgreementDocument) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Title != b[i].Title || a[i].ContentMD != b[i].ContentMD {
			return false
		}
	}
	return true
}

func equalIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalNotifyEmailEntries(a, b []service.NotifyEmailEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Email != b[i].Email || a[i].Verified != b[i].Verified || a[i].Disabled != b[i].Disabled {
			return false
		}
	}
	return true
}

func equalJSONComparable(a, b any) bool {
	aj, aerr := json.Marshal(a)
	bj, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(aj) == string(bj)
}

// TestSMTPRequest 测试SMTP连接请求
type TestSMTPRequest struct {
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPUseTLS   *bool  `json:"smtp_use_tls"`
}

func resolveSMTPUseTLS(requested *bool, savedConfig *service.SMTPConfig) bool {
	if requested != nil {
		return *requested
	}
	return savedConfig != nil && savedConfig.UseTLS
}

// TestSMTPConnection 测试SMTP连接
// POST /api/v1/admin/settings/test-smtp
func (h *SettingHandler) TestSMTPConnection(c *gin.Context) {
	var req TestSMTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)

	var savedConfig *service.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &service.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		UseTLS:   resolveSMTPUseTLS(req.SMTPUseTLS, savedConfig),
	}

	err := h.emailService.TestSMTPConnectionWithConfig(config)
	if err != nil {
		response.BadRequest(c, "SMTP connection test failed: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "SMTP connection successful"})
}

// SendTestEmailRequest 发送测试邮件请求
type SendTestEmailRequest struct {
	Email        string `json:"email" binding:"required,email"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password"`
	SMTPFrom     string `json:"smtp_from_email"`
	SMTPFromName string `json:"smtp_from_name"`
	SMTPUseTLS   *bool  `json:"smtp_use_tls"`
}

// SendTestEmail 发送测试邮件
// POST /api/v1/admin/settings/send-test-email
func (h *SettingHandler) SendTestEmail(c *gin.Context) {
	var req SendTestEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.SMTPHost = strings.TrimSpace(req.SMTPHost)
	req.SMTPUsername = strings.TrimSpace(req.SMTPUsername)
	req.SMTPFrom = strings.TrimSpace(req.SMTPFrom)
	req.SMTPFromName = strings.TrimSpace(req.SMTPFromName)

	var savedConfig *service.SMTPConfig
	if cfg, err := h.emailService.GetSMTPConfig(c.Request.Context()); err == nil && cfg != nil {
		savedConfig = cfg
	}

	if req.SMTPHost == "" && savedConfig != nil {
		req.SMTPHost = savedConfig.Host
	}
	if req.SMTPPort <= 0 {
		if savedConfig != nil && savedConfig.Port > 0 {
			req.SMTPPort = savedConfig.Port
		} else {
			req.SMTPPort = 587
		}
	}
	if req.SMTPUsername == "" && savedConfig != nil {
		req.SMTPUsername = savedConfig.Username
	}
	password := strings.TrimSpace(req.SMTPPassword)
	if password == "" && savedConfig != nil {
		password = savedConfig.Password
	}
	if req.SMTPFrom == "" && savedConfig != nil {
		req.SMTPFrom = savedConfig.From
	}
	if req.SMTPFromName == "" && savedConfig != nil {
		req.SMTPFromName = savedConfig.FromName
	}
	if req.SMTPHost == "" {
		response.BadRequest(c, "SMTP host is required")
		return
	}

	config := &service.SMTPConfig{
		Host:     req.SMTPHost,
		Port:     req.SMTPPort,
		Username: req.SMTPUsername,
		Password: password,
		From:     req.SMTPFrom,
		FromName: req.SMTPFromName,
		UseTLS:   resolveSMTPUseTLS(req.SMTPUseTLS, savedConfig),
	}

	siteName := h.settingService.GetSiteName(c.Request.Context())
	subject := "[" + siteName + "] Test Email"
	body := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; padding: 30px; text-align: center; }
        .content { padding: 40px 30px; text-align: center; }
        .success { color: #10b981; font-size: 48px; margin-bottom: 20px; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>` + html.EscapeString(siteName) + `</h1>
        </div>
        <div class="content">
            <div class="success">✓</div>
            <h2>Email Configuration Successful!</h2>
            <p>This is a test email to verify your SMTP settings are working correctly.</p>
        </div>
        <div class="footer">
            <p>This is an automated test message.</p>
        </div>
    </div>
</body>
</html>
`

	if err := h.emailService.SendEmailWithConfig(config, req.Email, subject, body); err != nil {
		response.BadRequest(c, "Failed to send test email: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "Test email sent successfully"})
}

// GetAdminAPIKey 获取管理员 API Key 状态
// GET /api/v1/admin/settings/admin-api-key
func (h *SettingHandler) GetAdminAPIKey(c *gin.Context) {
	if !requireAdminKeyJWT(c) {
		return
	}
	maskedKey, exists, err := h.settingService.GetAdminAPIKeyStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"exists":     exists,
		"masked_key": maskedKey,
	})
}

// RegenerateAdminAPIKey 轮换已存在的旧版管理员 API Key
// POST /api/v1/admin/settings/admin-api-key/regenerate
func (h *SettingHandler) RegenerateAdminAPIKey(c *gin.Context) {
	if !requireAdminKeyJWT(c) {
		return
	}
	_, exists, err := h.settingService.GetAdminAPIKeyStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !exists {
		response.Error(c, 403, "旧版密钥已停止新建，请创建权限管理密钥")
		return
	}
	key, err := h.settingService.GenerateAdminAPIKey(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"key": key, // 完整 key 只在生成时返回一次
	})
}

// DeleteAdminAPIKey 删除管理员 API Key
// DELETE /api/v1/admin/settings/admin-api-key
func (h *SettingHandler) DeleteAdminAPIKey(c *gin.Context) {
	if !requireAdminKeyJWT(c) {
		return
	}
	if err := h.settingService.DeleteAdminAPIKey(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Admin API key deleted"})
}

// GetOverloadCooldownSettings 获取529过载冷却配置
// GET /api/v1/admin/settings/overload-cooldown
func (h *SettingHandler) GetOverloadCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.OverloadCooldownSettings{
		Enabled:         settings.Enabled,
		CooldownMinutes: settings.CooldownMinutes,
	})
}

// UpdateOverloadCooldownSettingsRequest 更新529过载冷却配置请求
type UpdateOverloadCooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownMinutes int  `json:"cooldown_minutes"`
}

// UpdateOverloadCooldownSettings 更新529过载冷却配置
// PUT /api/v1/admin/settings/overload-cooldown
func (h *SettingHandler) UpdateOverloadCooldownSettings(c *gin.Context) {
	var req UpdateOverloadCooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.OverloadCooldownSettings{
		Enabled:         req.Enabled,
		CooldownMinutes: req.CooldownMinutes,
	}

	if err := h.settingService.SetOverloadCooldownSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetOverloadCooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.OverloadCooldownSettings{
		Enabled:         updatedSettings.Enabled,
		CooldownMinutes: updatedSettings.CooldownMinutes,
	})
}

// GetRateLimit429CooldownSettings 获取429默认回避配置
// GET /api/v1/admin/settings/rate-limit-429-cooldown
func (h *SettingHandler) GetRateLimit429CooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetRateLimit429CooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.RateLimit429CooldownSettings{
		Enabled:         settings.Enabled,
		CooldownSeconds: settings.CooldownSeconds,
	})
}

// UpdateRateLimit429CooldownSettingsRequest 更新429默认回避配置请求
type UpdateRateLimit429CooldownSettingsRequest struct {
	Enabled         bool `json:"enabled"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

// UpdateRateLimit429CooldownSettings 更新429默认回避配置
// PUT /api/v1/admin/settings/rate-limit-429-cooldown
func (h *SettingHandler) UpdateRateLimit429CooldownSettings(c *gin.Context) {
	var req UpdateRateLimit429CooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.RateLimit429CooldownSettings{
		Enabled:         req.Enabled,
		CooldownSeconds: req.CooldownSeconds,
	}

	if err := h.settingService.SetRateLimit429CooldownSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetRateLimit429CooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.RateLimit429CooldownSettings{
		Enabled:         updatedSettings.Enabled,
		CooldownSeconds: updatedSettings.CooldownSeconds,
	})
}

// GetOpenAIImagesOAuthUnavailableCooldownSettings returns the account-level
// cooldown used when the OpenAI Images OAuth tool is unavailable.
func (h *SettingHandler) GetOpenAIImagesOAuthUnavailableCooldownSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIImagesOAuthUnavailableCooldownSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: settings.CooldownMinutes})
}

// UpdateOpenAIImagesOAuthUnavailableCooldownSettings updates the account-level
// cooldown used when the OpenAI Images OAuth tool is unavailable.
type UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest struct {
	CooldownMinutes int `json:"cooldown_minutes"`
}

func (h *SettingHandler) UpdateOpenAIImagesOAuthUnavailableCooldownSettings(c *gin.Context) {
	var req UpdateOpenAIImagesOAuthUnavailableCooldownSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings := &service.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: req.CooldownMinutes}
	if err := h.settingService.SetOpenAIImagesOAuthUnavailableCooldownSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: settings.CooldownMinutes})
}

// GetPanelRateLimitSettings 获取面板 API 限流配置
// GET /api/v1/admin/settings/panel-rate-limit
func (h *SettingHandler) GetPanelRateLimitSettings(c *gin.Context) {
	settings, err := h.settingService.GetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PanelRateLimitSettings{
		Enabled:     settings.Enabled,
		UserRPM:     settings.UserRPM,
		HeavyRPM:    settings.HeavyRPM,
		ExemptAdmin: settings.ExemptAdmin,
		PublicIPRPM: settings.PublicIPRPM,
	})
}

type UpdatePanelRateLimitSettingsRequest struct {
	Enabled     bool `json:"enabled"`
	UserRPM     int  `json:"user_rpm"`
	HeavyRPM    int  `json:"heavy_rpm"`
	ExemptAdmin bool `json:"exempt_admin"`
	PublicIPRPM int  `json:"public_ip_rpm"`
}

// UpdatePanelRateLimitSettings 更新面板 API 限流配置
// PUT /api/v1/admin/settings/panel-rate-limit
func (h *SettingHandler) UpdatePanelRateLimitSettings(c *gin.Context) {
	var req UpdatePanelRateLimitSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.PanelRateLimitSettings{
		Enabled:     req.Enabled,
		UserRPM:     req.UserRPM,
		HeavyRPM:    req.HeavyRPM,
		ExemptAdmin: req.ExemptAdmin,
		PublicIPRPM: req.PublicIPRPM,
	}
	if err := h.settingService.SetPanelRateLimitSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updatedSettings, err := h.settingService.GetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.PanelRateLimitSettings{
		Enabled:     updatedSettings.Enabled,
		UserRPM:     updatedSettings.UserRPM,
		HeavyRPM:    updatedSettings.HeavyRPM,
		ExemptAdmin: updatedSettings.ExemptAdmin,
		PublicIPRPM: updatedSettings.PublicIPRPM,
	})
}

// GetStreamTimeoutSettings 获取流超时处理配置
// GET /api/v1/admin/settings/stream-timeout
func (h *SettingHandler) GetStreamTimeoutSettings(c *gin.Context) {
	settings, err := h.settingService.GetStreamTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.StreamTimeoutSettings{
		Enabled:                settings.Enabled,
		Action:                 settings.Action,
		TempUnschedMinutes:     settings.TempUnschedMinutes,
		ThresholdCount:         settings.ThresholdCount,
		ThresholdWindowMinutes: settings.ThresholdWindowMinutes,
	})
}

// GetRectifierSettings 获取请求整流器配置
// GET /api/v1/admin/settings/rectifier
func (h *SettingHandler) GetRectifierSettings(c *gin.Context) {
	settings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	patterns := settings.APIKeySignaturePatterns
	if patterns == nil {
		patterns = []string{}
	}
	response.Success(c, dto.RectifierSettings{
		Enabled:                  settings.Enabled,
		ThinkingSignatureEnabled: settings.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    settings.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   settings.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  patterns,
	})
}

// UpdateRectifierSettingsRequest 更新整流器配置请求
type UpdateRectifierSettingsRequest struct {
	Enabled                  bool     `json:"enabled"`
	ThinkingSignatureEnabled bool     `json:"thinking_signature_enabled"`
	ThinkingBudgetEnabled    bool     `json:"thinking_budget_enabled"`
	APIKeySignatureEnabled   bool     `json:"apikey_signature_enabled"`
	APIKeySignaturePatterns  []string `json:"apikey_signature_patterns"`
}

// UpdateRectifierSettings 更新请求整流器配置
// PUT /api/v1/admin/settings/rectifier
func (h *SettingHandler) UpdateRectifierSettings(c *gin.Context) {
	var req UpdateRectifierSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// 校验并清理自定义匹配关键词
	const maxPatterns = 50
	const maxPatternLen = 500
	if len(req.APIKeySignaturePatterns) > maxPatterns {
		response.BadRequest(c, "Too many signature patterns (max 50)")
		return
	}
	var cleanedPatterns []string
	for _, p := range req.APIKeySignaturePatterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxPatternLen {
			response.BadRequest(c, "Signature pattern too long (max 500 characters)")
			return
		}
		cleanedPatterns = append(cleanedPatterns, p)
	}

	settings := &service.RectifierSettings{
		Enabled:                  req.Enabled,
		ThinkingSignatureEnabled: req.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    req.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   req.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  cleanedPatterns,
	}

	if err := h.settingService.SetRectifierSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// 重新获取设置返回
	updatedSettings, err := h.settingService.GetRectifierSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	updatedPatterns := updatedSettings.APIKeySignaturePatterns
	if updatedPatterns == nil {
		updatedPatterns = []string{}
	}
	response.Success(c, dto.RectifierSettings{
		Enabled:                  updatedSettings.Enabled,
		ThinkingSignatureEnabled: updatedSettings.ThinkingSignatureEnabled,
		ThinkingBudgetEnabled:    updatedSettings.ThinkingBudgetEnabled,
		APIKeySignatureEnabled:   updatedSettings.APIKeySignatureEnabled,
		APIKeySignaturePatterns:  updatedPatterns,
	})
}

// GetBetaPolicySettings 获取 Beta 策略配置
// GET /api/v1/admin/settings/beta-policy
func (h *SettingHandler) GetBetaPolicySettings(c *gin.Context) {
	settings, err := h.settingService.GetBetaPolicySettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	rules := make([]dto.BetaPolicyRule, len(settings.Rules))
	for i, r := range settings.Rules {
		rules[i] = dto.BetaPolicyRule(r)
	}
	response.Success(c, dto.BetaPolicySettings{Rules: rules})
}

// UpdateBetaPolicySettingsRequest 更新 Beta 策略配置请求
type UpdateBetaPolicySettingsRequest struct {
	Rules []dto.BetaPolicyRule `json:"rules"`
}

// UpdateBetaPolicySettings 更新 Beta 策略配置
// PUT /api/v1/admin/settings/beta-policy
func (h *SettingHandler) UpdateBetaPolicySettings(c *gin.Context) {
	var req UpdateBetaPolicySettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	rules := make([]service.BetaPolicyRule, len(req.Rules))
	for i, r := range req.Rules {
		rules[i] = service.BetaPolicyRule(r)
	}

	settings := &service.BetaPolicySettings{Rules: rules}
	if err := h.settingService.SetBetaPolicySettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Re-fetch to return updated settings
	updated, err := h.settingService.GetBetaPolicySettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	outRules := make([]dto.BetaPolicyRule, len(updated.Rules))
	for i, r := range updated.Rules {
		outRules[i] = dto.BetaPolicyRule(r)
	}
	response.Success(c, dto.BetaPolicySettings{Rules: outRules})
}

// UpdateStreamTimeoutSettingsRequest 更新流超时配置请求
type UpdateStreamTimeoutSettingsRequest struct {
	Enabled                bool   `json:"enabled"`
	Action                 string `json:"action"`
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`
	ThresholdCount         int    `json:"threshold_count"`
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"`
}

// UpdateStreamTimeoutSettings 更新流超时处理配置
// PUT /api/v1/admin/settings/stream-timeout
func (h *SettingHandler) UpdateStreamTimeoutSettings(c *gin.Context) {
	var req UpdateStreamTimeoutSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings := &service.StreamTimeoutSettings{
		Enabled:                req.Enabled,
		Action:                 req.Action,
		TempUnschedMinutes:     req.TempUnschedMinutes,
		ThresholdCount:         req.ThresholdCount,
		ThresholdWindowMinutes: req.ThresholdWindowMinutes,
	}

	if err := h.settingService.SetStreamTimeoutSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// 重新获取设置返回
	updatedSettings, err := h.settingService.GetStreamTimeoutSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.StreamTimeoutSettings{
		Enabled:                updatedSettings.Enabled,
		Action:                 updatedSettings.Action,
		TempUnschedMinutes:     updatedSettings.TempUnschedMinutes,
		ThresholdCount:         updatedSettings.ThresholdCount,
		ThresholdWindowMinutes: updatedSettings.ThresholdWindowMinutes,
	})
}

// GetWebSearchEmulationConfig 获取 Web Search 模拟配置
// GET /api/v1/admin/settings/web-search-emulation
func (h *SettingHandler) GetWebSearchEmulationConfig(c *gin.Context) {
	cfg, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.PopulateWebSearchUsage(c.Request.Context(), cfg))
}

// UpdateWebSearchEmulationConfig 更新 Web Search 模拟配置
// PUT /api/v1/admin/settings/web-search-emulation
func (h *SettingHandler) UpdateWebSearchEmulationConfig(c *gin.Context) {
	var cfg service.WebSearchEmulationConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.settingService.SaveWebSearchEmulationConfig(c.Request.Context(), &cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Re-read (with sanitized api keys) to return current state
	updated, err := h.settingService.GetWebSearchEmulationConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.PopulateWebSearchUsage(c.Request.Context(), updated))
}

// ResetWebSearchUsage 重置指定 provider 的配额用量
// POST /api/v1/admin/settings/web-search-emulation/reset-usage
func (h *SettingHandler) ResetWebSearchUsage(c *gin.Context) {
	var req struct {
		ProviderType string `json:"provider_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.ProviderType == "" {
		response.BadRequest(c, "provider_type is required")
		return
	}
	if err := service.ResetWebSearchUsage(c.Request.Context(), req.ProviderType); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, nil)
}

// TestWebSearchEmulation 测试 Web Search 搜索
// POST /api/v1/admin/settings/web-search-emulation/test
func (h *SettingHandler) TestWebSearchEmulation(c *gin.Context) {
	var req struct {
		Query string `json:"query"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		req.Query = "搜索今年世界大事件"
	}

	result, err := service.TestWebSearch(c.Request.Context(), req.Query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ListEmailTemplates returns all editable notification email templates.
// GET /api/v1/admin/settings/email-templates
func (h *SettingHandler) ListEmailTemplates(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	events := h.notificationEmailService.ListEventInfos()
	templates, err := h.notificationEmailService.ListTemplates(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.EmailTemplateListResponse{
		Events:       emailTemplateEventOptionsToDTO(events),
		Locales:      h.notificationEmailService.SupportedLocales(),
		Templates:    emailTemplateSummariesToDTO(templates),
		Placeholders: emailTemplatePlaceholderUnion(events),
	})
}

// GetEmailTemplate returns one editable notification email template.
// GET /api/v1/admin/settings/email-templates/:event/:locale
func (h *SettingHandler) GetEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	tmpl, err := h.notificationEmailService.GetTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// UpdateEmailTemplate saves an override for one event/locale template.
// PUT /api/v1/admin/settings/email-templates/:event/:locale
func (h *SettingHandler) UpdateEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.UpdateEmailTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	tmpl, err := h.notificationEmailService.UpdateTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"), req.Subject, req.HTML)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// RestoreOfficialEmailTemplate removes an override and returns the built-in template.
// POST /api/v1/admin/settings/email-templates/:event/:locale/restore-official
func (h *SettingHandler) RestoreOfficialEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	tmpl, err := h.notificationEmailService.RestoreOfficialTemplate(c.Request.Context(), c.Param("event"), c.Param("locale"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, emailTemplateDetailToDTO(tmpl))
}

// PreviewEmailTemplate renders a template with safe sample variables without saving it.
// POST /api/v1/admin/settings/email-templates/preview
func (h *SettingHandler) PreviewEmailTemplate(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.PreviewEmailTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	preview, err := h.notificationEmailService.PreviewTemplate(c.Request.Context(), service.NotificationEmailPreviewInput{
		Event:     req.Event,
		Locale:    req.Locale,
		Subject:   req.Subject,
		HTML:      req.HTML,
		Variables: req.Variables,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailTemplatePreviewResponse{Subject: preview.Subject, HTML: preview.HTML})
}

// SendEmailBroadcast starts a low-RPM admin email broadcast.
// POST /api/v1/admin/settings/email-broadcasts
func (h *SettingHandler) SendEmailBroadcast(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.SendEmailBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		req.CreatedByUserID = subject.UserID
	}
	req.CreatedByEmail = c.GetString(middleware.ContextKeyAuthEmail)
	result, err := h.notificationEmailService.StartBroadcast(c.Request.Context(), service.NotificationEmailBroadcastInput(req))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.SendEmailBroadcastResponse(result))
}

func (h *SettingHandler) PreflightEmailBroadcast(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.SendEmailBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.notificationEmailService.PreflightBroadcast(c.Request.Context(), service.NotificationEmailBroadcastInput(req))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastPreflightResponse(result))
}

// PreviewEmailBroadcastRecipients returns audience counts for the compose form.
// POST /api/v1/admin/settings/email-broadcasts/preview
func (h *SettingHandler) PreviewEmailBroadcastRecipients(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.SendEmailBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.notificationEmailService.PreviewBroadcastRecipients(c.Request.Context(), service.NotificationEmailBroadcastInput(req))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastPreflightResponse(result))
}

func (h *SettingHandler) ListEmailBroadcastRecipients(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		page = 1
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "100"))
	if err != nil {
		pageSize = 100
	}
	result, err := h.notificationEmailService.ListBroadcastRecipients(c.Request.Context(), c.Param("batch_id"), c.Query("status"), page, pageSize)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastRecipientPageResponse(result))
}

// GetEmailBroadcastDraft returns the saved admin email broadcast draft.
// GET /api/v1/admin/settings/email-broadcasts/draft
func (h *SettingHandler) GetEmailBroadcastDraft(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	draft, err := h.notificationEmailService.GetBroadcastDraft(c.Request.Context())
	if err != nil {
		if errors.Is(err, service.ErrSettingNotFound) {
			response.Success(c, nil)
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastDraftResponse(draft))
}

// SaveEmailBroadcastDraft persists the current admin email broadcast compose form.
// PUT /api/v1/admin/settings/email-broadcasts/draft
func (h *SettingHandler) SaveEmailBroadcastDraft(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.SendEmailBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok {
		req.CreatedByUserID = subject.UserID
	}
	draft, err := h.notificationEmailService.SaveBroadcastDraft(c.Request.Context(), service.NotificationEmailBroadcastInput(req))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastDraftResponse(draft))
}

// DeleteEmailBroadcastDraft clears the saved admin email broadcast draft.
// DELETE /api/v1/admin/settings/email-broadcasts/draft
func (h *SettingHandler) DeleteEmailBroadcastDraft(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	if err := h.notificationEmailService.DeleteBroadcastDraft(c.Request.Context()); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ListEmailBroadcasts returns recent admin email broadcast jobs.
// GET /api/v1/admin/settings/email-broadcasts
func (h *SettingHandler) ListEmailBroadcasts(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	result, err := h.notificationEmailService.ListBroadcasts(c.Request.Context())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastListResponse(result))
}

// GetEmailBroadcastStatus returns the latest status for an admin email broadcast.
// GET /api/v1/admin/settings/email-broadcasts/:batch_id
func (h *SettingHandler) GetEmailBroadcastStatus(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	status, err := h.notificationEmailService.GetBroadcastStatus(c.Request.Context(), c.Param("batch_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastStatusResponse(status))
}

// CancelEmailBroadcast requests a running admin email broadcast to stop.
// POST /api/v1/admin/settings/email-broadcasts/:batch_id/cancel
func (h *SettingHandler) CancelEmailBroadcast(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	status, err := h.notificationEmailService.CancelBroadcast(c.Request.Context(), c.Param("batch_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.EmailBroadcastStatusResponse(status))
}

// ResumeEmailBroadcast resumes a stopped/interrupted broadcast job.
// POST /api/v1/admin/settings/email-broadcasts/:batch_id/resume
func (h *SettingHandler) ResumeEmailBroadcast(c *gin.Context) {
	if h.notificationEmailService == nil {
		response.InternalError(c, "notification email service is not configured")
		return
	}
	var req dto.ResumeEmailBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.notificationEmailService.ResumeBroadcast(c.Request.Context(), c.Param("batch_id"), service.NotificationEmailBroadcastResumeInput(req))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, dto.SendEmailBroadcastResponse(result))
}

// GetNotificationConfig returns the lightweight global notification config.
// GET /api/v1/admin/settings/notifications
func (h *SettingHandler) GetNotificationConfig(c *gin.Context) {
	if h.notificationService == nil {
		response.InternalError(c, "notification service is not configured")
		return
	}
	cfg, err := h.notificationService.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// UpdateNotificationConfig saves the lightweight global notification config.
// PUT /api/v1/admin/settings/notifications
func (h *SettingHandler) UpdateNotificationConfig(c *gin.Context) {
	if h.notificationService == nil {
		response.InternalError(c, "notification service is not configured")
		return
	}
	var req dto.UpdateNotificationConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.notificationService.UpdateConfig(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

// TestNotificationTransport sends a test notification through one configured transport.
// POST /api/v1/admin/settings/notifications/test
func (h *SettingHandler) TestNotificationTransport(c *gin.Context) {
	if h.notificationService == nil {
		response.InternalError(c, "notification service is not configured")
		return
	}
	var req dto.TestNotificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.notificationService.Test(c.Request.Context(), req.Transport); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func emailTemplateEventOptionsToDTO(events []service.NotificationEmailEventInfo) []dto.EmailTemplateEventOption {
	items := make([]dto.EmailTemplateEventOption, 0, len(events))
	for _, event := range events {
		items = append(items, dto.EmailTemplateEventOption{
			Value:       event.Event,
			Label:       event.Label,
			Description: event.Description,
			Category:    event.Category,
			Optional:    event.Optional,
		})
	}
	return items
}

func emailTemplateSummariesToDTO(templates []service.NotificationEmailTemplate) []dto.EmailTemplateSummary {
	items := make([]dto.EmailTemplateSummary, 0, len(templates))
	for _, tmpl := range templates {
		items = append(items, dto.EmailTemplateSummary{
			Event:     tmpl.Event,
			Locale:    tmpl.Locale,
			Subject:   tmpl.Subject,
			IsCustom:  tmpl.IsCustom,
			UpdatedAt: emailTemplateUpdatedAt(tmpl),
		})
	}
	return items
}

func emailTemplateDetailToDTO(tmpl service.NotificationEmailTemplate) dto.EmailTemplateDetail {
	return dto.EmailTemplateDetail{
		Event:        tmpl.Event,
		Locale:       tmpl.Locale,
		Subject:      tmpl.Subject,
		HTML:         tmpl.HTML,
		IsCustom:     tmpl.IsCustom,
		UpdatedAt:    emailTemplateUpdatedAt(tmpl),
		Placeholders: tmpl.Placeholders,
	}
}

func emailTemplateUpdatedAt(tmpl service.NotificationEmailTemplate) string {
	if tmpl.UpdatedAt == nil {
		return ""
	}
	return tmpl.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
}

func emailTemplatePlaceholderUnion(events []service.NotificationEmailEventInfo) []string {
	seen := make(map[string]struct{})
	placeholders := make([]string, 0)
	for _, event := range events {
		for _, placeholder := range event.Placeholders {
			if _, ok := seen[placeholder]; ok {
				continue
			}
			seen[placeholder] = struct{}{}
			placeholders = append(placeholders, placeholder)
		}
	}
	return placeholders
}

// equalNullableFloat compares two *float64 values treating nil as a distinct case.
func equalNullableFloat(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// slotOf returns the *float64 for the given window from a DefaultPlatformQuotaSetting.
func slotOf(s *service.DefaultPlatformQuotaSetting, win string) *float64 {
	if s == nil {
		return nil
	}
	switch win {
	case "daily":
		return s.DailyLimitUSD
	case "weekly":
		return s.WeeklyLimitUSD
	case "monthly":
		return s.MonthlyLimitUSD
	}
	return nil
}

// equalPlatformQuotaSettings reports whether two platform-quota maps are identical across all 12 slots.
func equalPlatformQuotaSettings(before, after map[string]*service.DefaultPlatformQuotaSetting) bool {
	for _, platform := range service.AllowedQuotaPlatforms {
		b := before[platform]
		a := after[platform]
		if !equalNullableFloat(slotOf(b, "daily"), slotOf(a, "daily")) {
			return false
		}
		if !equalNullableFloat(slotOf(b, "weekly"), slotOf(a, "weekly")) {
			return false
		}
		if !equalNullableFloat(slotOf(b, "monthly"), slotOf(a, "monthly")) {
			return false
		}
	}
	return true
}

func stringSetting(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
