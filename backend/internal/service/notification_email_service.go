package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	NotificationEmailEventAuthVerifyCode              = "auth.verify_code"
	NotificationEmailEventAuthPasswordReset           = "auth.password_reset"
	NotificationEmailEventNotificationEmailVerifyCode = "notification_email.verify_code"
	NotificationEmailEventSubscriptionPurchaseSuccess = "subscription.purchase_success"
	NotificationEmailEventSubscriptionExpiryReminder  = "subscription.expiry_reminder"
	NotificationEmailEventBalanceLow                  = "balance.low"
	NotificationEmailEventBalanceRechargeSuccess      = "balance.recharge_success"
	NotificationEmailEventAccountQuotaAlert           = "account.quota_alert"
	NotificationEmailEventContentModerationViolation  = "content_moderation.violation_notice"
	NotificationEmailEventContentModerationDisabled   = "content_moderation.account_disabled"
	NotificationEmailEventOpsAlert                    = "ops.alert"
	NotificationEmailEventOpsScheduledReport          = "ops.scheduled_report"
	NotificationEmailEventAdminBroadcast              = "admin.broadcast_email"
	NotificationEmailEventChannelMonitorFailed        = NotificationEventChannelMonitorFailed
	NotificationEmailEventChannelMonitorRecovered     = NotificationEventChannelMonitorRecovered

	notificationEmailTemplateKeyPrefix    = "notification_email_template:"
	notificationEmailPreferenceKeyPrefix  = "notification_email_preference:"
	notificationEmailDeliveryKeyPrefix    = "notification_email_delivery:"
	notificationEmailBroadcastKeyPrefix   = "notification_email_broadcast:"
	notificationEmailBroadcastIndexKey    = notificationEmailBroadcastKeyPrefix + "index"
	notificationEmailLocaleUserKeyPrefix  = "notification_email_locale:user:"
	notificationEmailLocaleEmailKeyPrefix = "notification_email_locale:email:"
	notificationEmailUnsubscribeSecretKey = "notification_email_unsubscribe_secret"
	notificationEmailDefaultLocale        = "en"
	notificationEmailLocaleChinese        = "zh"
	notificationEmailMaxSubjectLength     = 200
	notificationEmailMaxHTMLLength        = 30000
	notificationEmailUnsubscribeTTL       = 365 * 24 * time.Hour
	notificationEmailBroadcastDefaultRPM  = 6
	notificationEmailBroadcastMaxRPM      = 30
	notificationEmailBroadcastPageSize    = 500
	notificationEmailBroadcastStaleAfter  = 15 * time.Minute
	notificationEmailBroadcastActiveKey   = notificationEmailBroadcastKeyPrefix + "active"
	notificationEmailBroadcastMaxListed   = 50
	notificationEmailUnsubscribeAPIPath   = "/api/v1/settings/email-unsubscribe"
	notificationEmailUnsubscribePagePath  = "/email-unsubscribe"
)

var (
	notificationEmailPlaceholderPattern = regexp.MustCompile(`{{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*}}`)
	notificationEmailLocales            = []string{notificationEmailDefaultLocale, notificationEmailLocaleChinese}
	notificationEmailCommonPlaceholders = []string{"site_name", "recipient_name", "recipient_email"}
)

type NotificationEmailService struct {
	settingRepo  SettingRepository
	emailService *EmailService
	userRepo     UserRepository
	now          func() time.Time
}

type NotificationEmailEventInfo struct {
	Event        string   `json:"event"`
	Label        string   `json:"label"`
	Description  string   `json:"description"`
	Category     string   `json:"category"`
	Optional     bool     `json:"optional"`
	Placeholders []string `json:"placeholders"`
}

type NotificationEmailTemplate struct {
	Event        string     `json:"event"`
	Locale       string     `json:"locale"`
	Subject      string     `json:"subject"`
	HTML         string     `json:"html"`
	IsCustom     bool       `json:"is_custom"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Placeholders []string   `json:"placeholders"`
}

type NotificationEmailPreview struct {
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type NotificationEmailPreviewInput struct {
	Event     string            `json:"event"`
	Locale    string            `json:"locale"`
	Subject   string            `json:"subject"`
	HTML      string            `json:"html"`
	Variables map[string]string `json:"variables,omitempty"`
}

type NotificationEmailSendInput struct {
	Event            string
	Locale           string
	RecipientEmail   string
	RecipientName    string
	UserID           int64
	SourceType       string
	SourceID         string
	ReminderKey      string
	Variables        map[string]string
	RawHTMLVariables map[string]string
}

type NotificationEmailBroadcastInput struct {
	Scope        string   `json:"scope"`
	Locale       string   `json:"locale"`
	MessageTitle string   `json:"message_title"`
	MessageHTML  string   `json:"message_html"`
	ActionLabel  string   `json:"action_label,omitempty"`
	ActionURL    string   `json:"action_url,omitempty"`
	UserIDs      []int64  `json:"user_ids,omitempty"`
	Emails       []string `json:"emails,omitempty"`
	RPM          int      `json:"rpm"`
}

type NotificationEmailBroadcastResult struct {
	BatchID                  string `json:"batch_id"`
	TargetCount              int    `json:"target_count"`
	RPM                      int    `json:"rpm"`
	EstimatedDurationSeconds int    `json:"estimated_duration_seconds"`
	StartedAt                string `json:"started_at"`
}

type NotificationEmailBroadcastStatus struct {
	BatchID           string `json:"batch_id"`
	Status            string `json:"status"`
	Scope             string `json:"scope"`
	Locale            string `json:"locale"`
	MessageTitle      string `json:"message_title,omitempty"`
	TargetCount       int    `json:"target_count"`
	SentCount         int    `json:"sent_count"`
	SkippedCount      int    `json:"skipped_count"`
	UnsubscribedCount int    `json:"unsubscribed_count"`
	FailureCount      int    `json:"failure_count"`
	RPM               int    `json:"rpm"`
	StartedAt         string `json:"started_at"`
	UpdatedAt         string `json:"updated_at"`
	CompletedAt       string `json:"completed_at,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

type NotificationEmailBroadcastList struct {
	Jobs          []NotificationEmailBroadcastStatus `json:"jobs"`
	ActiveBatchID string                             `json:"active_batch_id,omitempty"`
}

type NotificationEmailBroadcastResumeInput struct {
	Mode string `json:"mode,omitempty"`
}

type notificationEmailBroadcastRecipient struct {
	UserID int64
	Email  string
	Name   string
}

type notificationEmailBroadcastRecipientState struct {
	UserID    int64  `json:"user_id,omitempty"`
	Email     string `json:"email"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type notificationEmailAtomicSettingRepository interface {
	SetIfAbsent(ctx context.Context, key, value string) (bool, error)
}

type NotificationEmailUnsubscribeResult struct {
	Event      string `json:"event"`
	EventLabel string `json:"event_label,omitempty"`
	Email      string `json:"email"`
	Done       bool   `json:"done"`
}

type notificationEmailStoredTemplate struct {
	Subject   string    `json:"subject"`
	HTML      string    `json:"html"`
	UpdatedAt time.Time `json:"updated_at"`
}

type notificationEmailOfficialTemplate struct {
	Subject string
	HTML    string
}

type notificationEmailTemplateError struct {
	Err error
}

func (e notificationEmailTemplateError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailTemplateError) Unwrap() error {
	return e.Err
}

type notificationEmailConfigError struct {
	Err error
}

func (e notificationEmailConfigError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailConfigError) Unwrap() error {
	return e.Err
}

type notificationEmailDeliveryError struct {
	Err error
}

func (e notificationEmailDeliveryError) Error() string {
	return e.Err.Error()
}

func (e notificationEmailDeliveryError) Unwrap() error {
	return e.Err
}

type notificationEmailUnsubscribeClaims struct {
	Email string `json:"email"`
	Event string `json:"event"`
	Exp   int64  `json:"exp"`
}

func NewNotificationEmailService(settingRepo SettingRepository, emailService *EmailService) *NotificationEmailService {
	svc := &NotificationEmailService{
		settingRepo:  settingRepo,
		emailService: emailService,
		now:          func() time.Time { return time.Now().UTC() },
	}
	if emailService != nil {
		emailService.SetNotificationEmailService(svc)
	}
	return svc
}

func (s *NotificationEmailService) SetUserRepository(userRepo UserRepository) {
	s.userRepo = userRepo
}

func (s *NotificationEmailService) nowUTC() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func notificationEmailTemplateErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailTemplateError{Err: err}
}

func notificationEmailConfigErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailConfigError{Err: err}
}

func notificationEmailDeliveryErr(err error) error {
	if err == nil {
		return nil
	}
	return notificationEmailDeliveryError{Err: err}
}

func shouldFallbackNotificationEmail(err error) bool {
	if err == nil {
		return false
	}
	var templateErr notificationEmailTemplateError
	if errors.As(err, &templateErr) {
		return true
	}
	var configErr notificationEmailConfigError
	return errors.As(err, &configErr)
}

func isNotificationEmailDeliveryError(err error) bool {
	var deliveryErr notificationEmailDeliveryError
	return errors.As(err, &deliveryErr)
}

func (s *NotificationEmailService) ListEventInfos() []NotificationEmailEventInfo {
	infos := make([]NotificationEmailEventInfo, 0, len(notificationEmailEventDefinitions))
	for _, event := range notificationEmailEventOrder {
		info := notificationEmailEventDefinitions[event]
		info.Placeholders = append([]string(nil), info.Placeholders...)
		infos = append(infos, info)
	}
	return infos
}

func (s *NotificationEmailService) SupportedLocales() []string {
	return append([]string(nil), notificationEmailLocales...)
}

func (s *NotificationEmailService) ListTemplates(ctx context.Context) ([]NotificationEmailTemplate, error) {
	items := make([]NotificationEmailTemplate, 0, len(notificationEmailEventOrder)*len(notificationEmailLocales))
	for _, event := range notificationEmailEventOrder {
		for _, locale := range notificationEmailLocales {
			tmpl, err := s.GetTemplate(ctx, event, locale)
			if err != nil {
				return nil, err
			}
			items = append(items, tmpl)
		}
	}
	return items, nil
}

func (s *NotificationEmailService) GetTemplate(ctx context.Context, event, locale string) (NotificationEmailTemplate, error) {
	info, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	official, ok := notificationEmailOfficialTemplates[normalizedEvent][normalizedLocale]
	if !ok {
		return NotificationEmailTemplate{}, fmt.Errorf("official template not found for %s/%s", normalizedEvent, normalizedLocale)
	}

	tmpl := NotificationEmailTemplate{
		Event:        normalizedEvent,
		Locale:       normalizedLocale,
		Subject:      official.Subject,
		HTML:         official.HTML,
		Placeholders: append([]string(nil), info.Placeholders...),
	}

	raw, err := s.settingRepo.GetValue(ctx, notificationEmailTemplateKey(normalizedEvent, normalizedLocale))
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return tmpl, nil
		}
		return NotificationEmailTemplate{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return tmpl, nil
	}

	var stored notificationEmailStoredTemplate
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return NotificationEmailTemplate{}, fmt.Errorf("decode email template override: %w", err)
	}
	if err := validateNotificationEmailTemplate(normalizedEvent, stored.Subject, stored.HTML); err != nil {
		return NotificationEmailTemplate{}, err
	}
	tmpl.Subject = stored.Subject
	tmpl.HTML = stored.HTML
	tmpl.IsCustom = true
	updatedAt := stored.UpdatedAt
	tmpl.UpdatedAt = &updatedAt
	return tmpl, nil
}

func (s *NotificationEmailService) UpdateTemplate(ctx context.Context, event, locale, subject, htmlBody string) (NotificationEmailTemplate, error) {
	_, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	if err := validateNotificationEmailTemplate(normalizedEvent, subject, htmlBody); err != nil {
		return NotificationEmailTemplate{}, err
	}
	stored := notificationEmailStoredTemplate{
		Subject:   strings.TrimSpace(subject),
		HTML:      htmlBody,
		UpdatedAt: s.nowUTC(),
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	if err := s.settingRepo.Set(ctx, notificationEmailTemplateKey(normalizedEvent, normalizedLocale), string(payload)); err != nil {
		return NotificationEmailTemplate{}, err
	}
	return s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
}

func (s *NotificationEmailService) RestoreOfficialTemplate(ctx context.Context, event, locale string) (NotificationEmailTemplate, error) {
	_, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return NotificationEmailTemplate{}, err
	}
	normalizedLocale := normalizeNotificationLocale(locale)
	if err := s.settingRepo.Delete(ctx, notificationEmailTemplateKey(normalizedEvent, normalizedLocale)); err != nil && !errors.Is(err, ErrSettingNotFound) {
		return NotificationEmailTemplate{}, err
	}
	return s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
}

func (s *NotificationEmailService) PreviewTemplate(ctx context.Context, input NotificationEmailPreviewInput) (NotificationEmailPreview, error) {
	_, normalizedEvent, err := s.eventInfo(input.Event)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	normalizedLocale := normalizeNotificationLocale(input.Locale)
	subject := input.Subject
	htmlBody := input.HTML
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(htmlBody) == "" {
		tmpl, err := s.GetTemplate(ctx, normalizedEvent, normalizedLocale)
		if err != nil {
			return NotificationEmailPreview{}, err
		}
		if strings.TrimSpace(subject) == "" {
			subject = tmpl.Subject
		}
		if strings.TrimSpace(htmlBody) == "" {
			htmlBody = tmpl.HTML
		}
	}
	if err := validateNotificationEmailTemplate(normalizedEvent, subject, htmlBody); err != nil {
		return NotificationEmailPreview{}, err
	}
	variables := s.sampleVariables(ctx, normalizedEvent, normalizedLocale)
	for key, value := range input.Variables {
		variables[key] = value
	}
	return renderNotificationEmail(normalizedEvent, subject, htmlBody, variables, previewRawHTMLVariables(normalizedEvent, variables))
}

func (s *NotificationEmailService) Send(ctx context.Context, input NotificationEmailSendInput) error {
	info, normalizedEvent, err := s.eventInfo(input.Event)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}
	recipient := strings.TrimSpace(input.RecipientEmail)
	if recipient == "" {
		return nil
	}
	if info.Optional {
		unsubscribed, err := s.IsUnsubscribed(ctx, recipient, normalizedEvent)
		if err != nil {
			return err
		}
		if unsubscribed {
			slog.Info("notification email suppressed by unsubscribe preference", "event", normalizedEvent, "recipient_hash", notificationEmailHash(recipient))
			return nil
		}
	}

	locale := normalizeNotificationLocale(input.Locale)
	if strings.TrimSpace(input.Locale) == "" {
		locale = s.ResolveRecipientLocale(ctx, input.UserID, recipient)
	}
	tmpl, err := s.GetTemplate(ctx, normalizedEvent, locale)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}
	variables, headers, err := s.runtimeVariables(ctx, normalizedEvent, locale, input)
	if err != nil {
		return notificationEmailConfigErr(err)
	}
	rendered, err := renderNotificationEmail(normalizedEvent, tmpl.Subject, tmpl.HTML, variables, input.RawHTMLVariables)
	if err != nil {
		return notificationEmailTemplateErr(err)
	}

	deliveryKey := notificationEmailDeliveryKey(normalizedEvent, input.SourceType, input.SourceID, recipient, input.ReminderKey)
	if deliveryKey != "" {
		sent, err := s.deliveryExists(ctx, deliveryKey, legacyNotificationEmailDeliveryKey(normalizedEvent, input.SourceType, input.SourceID, recipient, input.ReminderKey))
		if err != nil {
			return err
		}
		if sent {
			return nil
		}
	}

	if s.emailService == nil {
		return notificationEmailConfigErr(errors.New("email service is not configured"))
	}
	if err := s.emailService.SendEmailWithHeaders(ctx, recipient, rendered.Subject, rendered.HTML, headers); err != nil {
		return notificationEmailDeliveryErr(err)
	}
	if deliveryKey != "" {
		if err := s.settingRepo.Set(ctx, deliveryKey, s.nowUTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

func (s *NotificationEmailService) StartBroadcast(ctx context.Context, input NotificationEmailBroadcastInput) (NotificationEmailBroadcastResult, error) {
	if s == nil {
		return NotificationEmailBroadcastResult{}, errors.New("notification email service is not configured")
	}
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.Locale = normalizeNotificationLocale(input.Locale)
	input.MessageTitle = strings.TrimSpace(input.MessageTitle)
	input.MessageHTML = strings.TrimSpace(input.MessageHTML)
	input.ActionLabel = strings.TrimSpace(input.ActionLabel)
	input.ActionURL = strings.TrimSpace(input.ActionURL)
	input.RPM = normalizeNotificationEmailBroadcastRPM(input.RPM)
	if input.MessageTitle == "" {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast message title is required")
	}
	if len([]rune(input.MessageTitle)) > notificationEmailMaxSubjectLength {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("broadcast message title cannot exceed %d characters", notificationEmailMaxSubjectLength)
	}
	if input.MessageHTML == "" {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast message html is required")
	}
	if len([]byte(input.MessageHTML)) > notificationEmailMaxHTMLLength {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("broadcast message html cannot exceed %d bytes", notificationEmailMaxHTMLLength)
	}
	if input.ActionURL != "" && !isSafeNotificationEmailURL(input.ActionURL) {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast action url must be http(s), mailto, or a relative path")
	}
	if s.emailService == nil {
		return NotificationEmailBroadcastResult{}, errors.New("email service is not configured")
	}
	if _, err := s.emailService.GetSMTPConfig(ctx); err != nil {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("email service is not configured: %w", err)
	}

	recipients, err := s.resolveBroadcastRecipients(ctx, input)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if len(recipients) == 0 {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast requires at least one recipient")
	}
	if err := s.releaseInterruptedActiveBroadcast(ctx, s.nowUTC()); err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	batchID, err := notificationEmailBroadcastBatchID()
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	locked, err := s.acquireBroadcastLock(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if !locked {
		return NotificationEmailBroadcastResult{}, errors.New("another email broadcast is already running")
	}
	lockReleased := false
	releaseLock := func() {
		if !lockReleased {
			s.releaseBroadcastLockBestEffort(ctx, batchID)
			lockReleased = true
		}
	}
	startedAt := s.nowUTC()
	result := NotificationEmailBroadcastResult{
		BatchID:                  batchID,
		TargetCount:              len(recipients),
		RPM:                      input.RPM,
		EstimatedDurationSeconds: notificationEmailBroadcastEstimateSeconds(len(recipients), input.RPM),
		StartedAt:                startedAt.Format(time.RFC3339),
	}
	status := NotificationEmailBroadcastStatus{
		BatchID:      batchID,
		Status:       "running",
		Scope:        input.Scope,
		Locale:       input.Locale,
		MessageTitle: input.MessageTitle,
		TargetCount:  len(recipients),
		RPM:          input.RPM,
		StartedAt:    result.StartedAt,
		UpdatedAt:    result.StartedAt,
	}
	if err := s.saveBroadcastPayload(ctx, batchID, input); err != nil {
		releaseLock()
		return NotificationEmailBroadcastResult{}, err
	}
	if err := s.saveBroadcastRecipients(ctx, batchID, notificationEmailBroadcastInitialRecipientStates(recipients)); err != nil {
		releaseLock()
		return NotificationEmailBroadcastResult{}, err
	}
	if err := s.saveBroadcastStatus(ctx, status); err != nil {
		releaseLock()
		return NotificationEmailBroadcastResult{}, err
	}
	if err := s.addBroadcastToIndex(ctx, batchID); err != nil {
		releaseLock()
		return NotificationEmailBroadcastResult{}, err
	}

	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	go s.runBroadcast(base, batchID, input, recipients, startedAt)
	return result, nil
}

func (s *NotificationEmailService) GetBroadcastStatus(ctx context.Context, batchID string) (NotificationEmailBroadcastStatus, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailBroadcastStatus{}, errors.New("notification email service is not configured")
	}
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return NotificationEmailBroadcastStatus{}, errors.New("broadcast batch id is required")
	}
	status, err := s.getBroadcastStatusRaw(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	status = s.interruptBroadcastStatusIfStale(ctx, status, s.nowUTC())
	return status, nil
}

func (s *NotificationEmailService) ListBroadcasts(ctx context.Context) (NotificationEmailBroadcastList, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailBroadcastList{}, errors.New("notification email service is not configured")
	}
	now := s.nowUTC()
	statuses, err := s.listBroadcastStatuses(ctx, now)
	if err != nil {
		return NotificationEmailBroadcastList{}, err
	}
	activeBatchID := ""
	if active, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastActiveKey); err == nil {
		activeBatchID = strings.TrimSpace(active)
	} else if !errors.Is(err, ErrSettingNotFound) {
		return NotificationEmailBroadcastList{}, err
	}
	return NotificationEmailBroadcastList{Jobs: statuses, ActiveBatchID: activeBatchID}, nil
}

func (s *NotificationEmailService) CancelBroadcast(ctx context.Context, batchID string) (NotificationEmailBroadcastStatus, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailBroadcastStatus{}, errors.New("notification email service is not configured")
	}
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return NotificationEmailBroadcastStatus{}, errors.New("broadcast batch id is required")
	}
	status, err := s.GetBroadcastStatus(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	if status.Status != "running" && status.Status != "canceling" {
		return status, nil
	}
	if err := s.settingRepo.Set(ctx, notificationEmailBroadcastCancelKey(batchID), s.nowUTC().Format(time.RFC3339)); err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	status.Status = "canceling"
	status.LastError = "broadcast cancellation requested"
	if err := s.saveBroadcastStatus(ctx, status); err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	return status, nil
}

func (s *NotificationEmailService) ResumeBroadcast(ctx context.Context, batchID string, input NotificationEmailBroadcastResumeInput) (NotificationEmailBroadcastResult, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailBroadcastResult{}, errors.New("notification email service is not configured")
	}
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast batch id is required")
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = "remaining"
	}
	if mode != "remaining" && mode != "failed" {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("unsupported broadcast resume mode: %s", input.Mode)
	}
	if err := s.releaseInterruptedActiveBroadcast(ctx, s.nowUTC()); err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	status, err := s.GetBroadcastStatus(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if status.Status == "running" || status.Status == "canceling" {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast is already running")
	}
	payload, err := s.getBroadcastPayload(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	states, err := s.getBroadcastRecipients(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	targets := notificationEmailBroadcastRetryRecipients(states, mode)
	if len(targets) == 0 {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast has no recipients to send")
	}
	locked, err := s.acquireBroadcastLock(ctx, batchID)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if !locked {
		return NotificationEmailBroadcastResult{}, errors.New("another email broadcast is already running")
	}
	if err := s.settingRepo.Delete(ctx, notificationEmailBroadcastCancelKey(batchID)); err != nil && !errors.Is(err, ErrSettingNotFound) {
		s.releaseBroadcastLockBestEffort(ctx, batchID)
		return NotificationEmailBroadcastResult{}, err
	}

	startedAt := s.nowUTC()
	status.Status = "running"
	status.MessageTitle = payload.MessageTitle
	status.CompletedAt = ""
	status.LastError = ""
	notificationEmailBroadcastApplyCounts(&status, states)
	if err := s.saveBroadcastStatus(ctx, status); err != nil {
		s.releaseBroadcastLockBestEffort(ctx, batchID)
		return NotificationEmailBroadcastResult{}, err
	}
	result := NotificationEmailBroadcastResult{
		BatchID:                  batchID,
		TargetCount:              len(targets),
		RPM:                      payload.RPM,
		EstimatedDurationSeconds: notificationEmailBroadcastEstimateSeconds(len(targets), payload.RPM),
		StartedAt:                startedAt.Format(time.RFC3339),
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	go s.runBroadcast(base, batchID, payload, targets, startedAt)
	return result, nil
}

func (s *NotificationEmailService) RememberRecipientLocale(ctx context.Context, userID int64, email, acceptLanguage string) {
	locale := normalizeNotificationLocale(acceptLanguage)
	if strings.TrimSpace(acceptLanguage) == "" || s == nil || s.settingRepo == nil {
		return
	}
	if userID > 0 {
		_ = s.settingRepo.Set(ctx, notificationEmailLocaleUserKeyPrefix+strconv.FormatInt(userID, 10), locale)
	}
	if emailHash := notificationEmailHash(email); emailHash != "" {
		_ = s.settingRepo.Set(ctx, notificationEmailLocaleEmailKeyPrefix+emailHash, locale)
	}
}

func (s *NotificationEmailService) ResolveRecipientLocale(ctx context.Context, userID int64, email string) string {
	if s == nil || s.settingRepo == nil {
		return notificationEmailDefaultLocale
	}
	if userID > 0 {
		if locale, err := s.settingRepo.GetValue(ctx, notificationEmailLocaleUserKeyPrefix+strconv.FormatInt(userID, 10)); err == nil && strings.TrimSpace(locale) != "" {
			return normalizeNotificationLocale(locale)
		}
	}
	if emailHash := notificationEmailHash(email); emailHash != "" {
		if locale, err := s.settingRepo.GetValue(ctx, notificationEmailLocaleEmailKeyPrefix+emailHash); err == nil && strings.TrimSpace(locale) != "" {
			return normalizeNotificationLocale(locale)
		}
	}
	return notificationEmailDefaultLocale
}

func (s *NotificationEmailService) resolveBroadcastRecipients(ctx context.Context, input NotificationEmailBroadcastInput) ([]notificationEmailBroadcastRecipient, error) {
	recipients := make([]notificationEmailBroadcastRecipient, 0)
	seen := map[string]struct{}{}
	addRecipient := func(recipient notificationEmailBroadcastRecipient) {
		recipient.Email = strings.TrimSpace(recipient.Email)
		if recipient.Email == "" {
			return
		}
		key := strings.ToLower(recipient.Email)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(recipient.Name) == "" {
			recipient.Name = emailRecipientName(recipient.Email)
		}
		recipients = append(recipients, recipient)
	}

	switch input.Scope {
	case "", "active_users":
		if s.userRepo == nil {
			return nil, errors.New("user repository is not configured")
		}
		if err := s.collectBroadcastUsers(ctx, UserListFilters{Status: StatusActive, Role: RoleUser}, addRecipient); err != nil {
			return nil, err
		}
	case "all_users":
		if s.userRepo == nil {
			return nil, errors.New("user repository is not configured")
		}
		if err := s.collectBroadcastUsers(ctx, UserListFilters{Role: RoleUser}, addRecipient); err != nil {
			return nil, err
		}
	case "admins":
		if s.userRepo == nil {
			return nil, errors.New("user repository is not configured")
		}
		if err := s.collectBroadcastUsers(ctx, UserListFilters{Status: StatusActive, Role: RoleAdmin}, addRecipient); err != nil {
			return nil, err
		}
	case "custom":
		if s.userRepo != nil {
			for _, userID := range input.UserIDs {
				if userID <= 0 {
					continue
				}
				user, err := s.userRepo.GetByID(ctx, userID)
				if err != nil {
					return nil, fmt.Errorf("load broadcast user %d: %w", userID, err)
				}
				addRecipient(notificationEmailBroadcastRecipient{UserID: user.ID, Email: user.Email, Name: user.Username})
			}
		}
		for _, email := range input.Emails {
			addRecipient(notificationEmailBroadcastRecipient{Email: email})
		}
	default:
		return nil, fmt.Errorf("unsupported broadcast scope: %s", input.Scope)
	}
	return recipients, nil
}

func (s *NotificationEmailService) collectBroadcastUsers(ctx context.Context, filters UserListFilters, addRecipient func(notificationEmailBroadcastRecipient)) error {
	if s == nil || s.userRepo == nil {
		return errors.New("user repository is not configured")
	}
	includeSubscriptions := false
	page := 1
	for {
		filters.IncludeSubscriptions = &includeSubscriptions
		users, result, err := s.userRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  notificationEmailBroadcastPageSize,
			SortBy:    "id",
			SortOrder: "asc",
		}, filters)
		if err != nil {
			return err
		}
		for i := range users {
			addRecipient(notificationEmailBroadcastRecipient{
				UserID: users[i].ID,
				Email:  users[i].Email,
				Name:   users[i].Username,
			})
		}
		if result == nil || int64(page*notificationEmailBroadcastPageSize) >= result.Total {
			return nil
		}
		page++
	}
}

func (s *NotificationEmailService) runBroadcast(ctx context.Context, batchID string, input NotificationEmailBroadcastInput, recipients []notificationEmailBroadcastRecipient, startedAt time.Time) {
	defer s.releaseBroadcastLockBestEffort(ctx, batchID)
	delay := time.Minute / time.Duration(input.RPM)
	status := NotificationEmailBroadcastStatus{
		BatchID:      batchID,
		Status:       "running",
		Scope:        input.Scope,
		Locale:       input.Locale,
		MessageTitle: input.MessageTitle,
		RPM:          input.RPM,
		StartedAt:    startedAt.UTC().Format(time.RFC3339),
	}
	if existingStatus, err := s.getBroadcastStatusRaw(ctx, batchID); err == nil {
		status = existingStatus
		status.Status = "running"
		status.Scope = input.Scope
		status.Locale = input.Locale
		status.MessageTitle = input.MessageTitle
		status.RPM = input.RPM
		if strings.TrimSpace(status.StartedAt) == "" {
			status.StartedAt = startedAt.UTC().Format(time.RFC3339)
		}
	}

	states, err := s.getBroadcastRecipients(ctx, batchID)
	if err != nil {
		states = notificationEmailBroadcastInitialRecipientStates(recipients)
		s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
	}
	if len(states) == 0 {
		states = notificationEmailBroadcastInitialRecipientStates(recipients)
		s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
	}
	stateByEmail := notificationEmailBroadcastStateByEmail(states)
	targets := make([]*notificationEmailBroadcastRecipientState, 0, len(recipients))
	for _, recipient := range recipients {
		key := strings.ToLower(strings.TrimSpace(recipient.Email))
		if key == "" {
			continue
		}
		state, ok := stateByEmail[key]
		if !ok {
			states = append(states, notificationEmailBroadcastRecipientState{
				UserID: recipient.UserID,
				Email:  strings.TrimSpace(recipient.Email),
				Name:   recipient.Name,
				Status: "pending",
			})
			stateByEmail = notificationEmailBroadcastStateByEmail(states)
			state = stateByEmail[key]
		}
		if state.Status == "sent" || state.Status == "skipped" {
			continue
		}
		targets = append(targets, state)
	}
	status.TargetCount = len(states)
	notificationEmailBroadcastApplyCounts(&status, states)
	s.saveBroadcastStatusBestEffort(ctx, status)

	for idx, state := range targets {
		if s.isBroadcastCancelRequested(ctx, batchID) {
			s.completeBroadcastAsCanceled(ctx, batchID, status, states)
			return
		}
		if idx > 0 && delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Warn("notification email broadcast canceled", "batch_id", batchID, "error", ctx.Err())
				status.LastError = ctx.Err().Error()
				s.completeBroadcastAsCanceled(ctx, batchID, status, states)
				return
			case <-timer.C:
			}
		}
		if s.isBroadcastCancelRequested(ctx, batchID) {
			s.completeBroadcastAsCanceled(ctx, batchID, status, states)
			return
		}
		recipient := notificationEmailBroadcastRecipient{
			UserID: state.UserID,
			Email:  state.Email,
			Name:   state.Name,
		}
		unsubscribed, err := s.IsUnsubscribed(ctx, recipient.Email, NotificationEmailEventAdminBroadcast)
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
			state.UpdatedAt = s.nowUTC().Format(time.RFC3339)
			status.LastError = err.Error()
			slog.Warn("notification email broadcast unsubscribe check failed", "batch_id", batchID, "recipient_hash", notificationEmailHash(recipient.Email), "error", err)
			notificationEmailBroadcastApplyCounts(&status, states)
			s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
			s.saveBroadcastStatusBestEffort(ctx, status)
			continue
		}
		if unsubscribed {
			state.Status = "skipped"
			state.Error = "unsubscribed"
			state.UpdatedAt = s.nowUTC().Format(time.RFC3339)
			slog.Info("notification email broadcast recipient skipped by unsubscribe preference", "batch_id", batchID, "recipient_hash", notificationEmailHash(recipient.Email))
			notificationEmailBroadcastApplyCounts(&status, states)
			s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
			s.saveBroadcastStatusBestEffort(ctx, status)
			continue
		}
		err = s.Send(ctx, NotificationEmailSendInput{
			Event:          NotificationEmailEventAdminBroadcast,
			Locale:         input.Locale,
			RecipientEmail: recipient.Email,
			RecipientName:  recipient.Name,
			UserID:         recipient.UserID,
			SourceType:     "admin_broadcast",
			SourceID:       batchID,
			Variables: map[string]string{
				"message_title": input.MessageTitle,
				"action_label":  input.ActionLabel,
				"action_url":    input.ActionURL,
			},
			RawHTMLVariables: map[string]string{
				"message_html": input.MessageHTML,
				"action_html":  notificationEmailBroadcastActionHTML(input.ActionLabel, input.ActionURL),
			},
		})
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
			state.UpdatedAt = s.nowUTC().Format(time.RFC3339)
			status.LastError = err.Error()
			slog.Warn("notification email broadcast recipient failed", "batch_id", batchID, "recipient_hash", notificationEmailHash(recipient.Email), "error", err)
		} else {
			state.Status = "sent"
			state.Error = ""
			state.UpdatedAt = s.nowUTC().Format(time.RFC3339)
		}
		notificationEmailBroadcastApplyCounts(&status, states)
		s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
		s.saveBroadcastStatusBestEffort(ctx, status)
	}
	status.Status = "completed"
	notificationEmailBroadcastApplyCounts(&status, states)
	status.CompletedAt = s.nowUTC().Format(time.RFC3339)
	s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
	s.saveBroadcastStatusBestEffort(ctx, status)
	slog.Info("notification email broadcast completed", "batch_id", batchID, "target_count", len(states), "sent", status.SentCount, "skipped", status.SkippedCount, "unsubscribed", status.UnsubscribedCount, "failed", status.FailureCount, "rpm", input.RPM)
}

func (s *NotificationEmailService) listBroadcastStatuses(ctx context.Context, now time.Time) ([]NotificationEmailBroadcastStatus, error) {
	all, err := s.settingRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	batchIDs := make([]string, 0)
	appendBatch := func(batchID string) {
		batchID = strings.TrimSpace(batchID)
		if batchID == "" {
			return
		}
		if _, ok := seen[batchID]; ok {
			return
		}
		seen[batchID] = struct{}{}
		batchIDs = append(batchIDs, batchID)
	}
	for _, batchID := range s.getBroadcastIndex(ctx) {
		appendBatch(batchID)
	}
	for key := range all {
		if !strings.HasPrefix(key, notificationEmailBroadcastKeyPrefix+"broadcast_") {
			continue
		}
		batchID := strings.TrimPrefix(key, notificationEmailBroadcastKeyPrefix)
		if strings.Contains(batchID, ":") {
			continue
		}
		appendBatch(batchID)
	}
	statuses := make([]NotificationEmailBroadcastStatus, 0, len(batchIDs))
	for _, batchID := range batchIDs {
		status, err := s.getBroadcastStatusRaw(ctx, batchID)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				continue
			}
			return nil, err
		}
		status = s.interruptBroadcastStatusIfStale(ctx, status, now)
		statuses = append(statuses, status)
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		return notificationEmailBroadcastSortTime(statuses[i]).After(notificationEmailBroadcastSortTime(statuses[j]))
	})
	if len(statuses) > notificationEmailBroadcastMaxListed {
		statuses = statuses[:notificationEmailBroadcastMaxListed]
	}
	return statuses, nil
}

func (s *NotificationEmailService) getBroadcastIndex(ctx context.Context) []string {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	raw, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastIndexKey)
	if err != nil {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *NotificationEmailService) addBroadcastToIndex(ctx context.Context, batchID string) error {
	ids := append([]string{strings.TrimSpace(batchID)}, s.getBroadcastIndex(ctx)...)
	out := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= notificationEmailBroadcastMaxListed {
			break
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, notificationEmailBroadcastIndexKey, string(raw))
}

func (s *NotificationEmailService) saveBroadcastPayload(ctx context.Context, batchID string, input NotificationEmailBroadcastInput) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, notificationEmailBroadcastPayloadKey(batchID), string(raw))
}

func (s *NotificationEmailService) getBroadcastPayload(ctx context.Context, batchID string) (NotificationEmailBroadcastInput, error) {
	raw, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastPayloadKey(batchID))
	if err != nil {
		return NotificationEmailBroadcastInput{}, err
	}
	var input NotificationEmailBroadcastInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return NotificationEmailBroadcastInput{}, err
	}
	input.RPM = normalizeNotificationEmailBroadcastRPM(input.RPM)
	input.Locale = normalizeNotificationLocale(input.Locale)
	return input, nil
}

func (s *NotificationEmailService) getBroadcastRecipients(ctx context.Context, batchID string) ([]notificationEmailBroadcastRecipientState, error) {
	raw, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastRecipientsKey(batchID))
	if err != nil {
		return nil, err
	}
	var states []notificationEmailBroadcastRecipientState
	if err := json.Unmarshal([]byte(raw), &states); err != nil {
		return nil, err
	}
	return states, nil
}

func (s *NotificationEmailService) saveBroadcastRecipients(ctx context.Context, batchID string, states []notificationEmailBroadcastRecipientState) error {
	raw, err := json.Marshal(states)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, notificationEmailBroadcastRecipientsKey(batchID), string(raw))
}

func (s *NotificationEmailService) saveBroadcastRecipientsBestEffort(ctx context.Context, batchID string, states []notificationEmailBroadcastRecipientState) {
	if err := s.saveBroadcastRecipients(ctx, batchID, states); err != nil {
		slog.Warn("notification email broadcast recipient state update failed", "batch_id", batchID, "error", err)
	}
}

func (s *NotificationEmailService) isBroadcastCancelRequested(ctx context.Context, batchID string) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	_, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastCancelKey(batchID))
	return err == nil
}

func (s *NotificationEmailService) completeBroadcastAsCanceled(ctx context.Context, batchID string, status NotificationEmailBroadcastStatus, states []notificationEmailBroadcastRecipientState) {
	status.Status = "canceled"
	status.LastError = "broadcast canceled by admin"
	status.CompletedAt = s.nowUTC().Format(time.RFC3339)
	notificationEmailBroadcastApplyCounts(&status, states)
	s.saveBroadcastRecipientsBestEffort(ctx, batchID, states)
	s.saveBroadcastStatusBestEffort(ctx, status)
	if err := s.settingRepo.Delete(ctx, notificationEmailBroadcastCancelKey(batchID)); err != nil && !errors.Is(err, ErrSettingNotFound) {
		slog.Warn("notification email broadcast cancel flag cleanup failed", "batch_id", batchID, "error", err)
	}
}

func (s *NotificationEmailService) saveBroadcastStatus(ctx context.Context, status NotificationEmailBroadcastStatus) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("notification email service is not configured")
	}
	now := s.nowUTC().Format(time.RFC3339)
	if strings.TrimSpace(status.StartedAt) == "" {
		status.StartedAt = now
	}
	status.UpdatedAt = now
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, notificationEmailBroadcastKey(status.BatchID), string(raw))
}

func (s *NotificationEmailService) saveBroadcastStatusBestEffort(ctx context.Context, status NotificationEmailBroadcastStatus) {
	if err := s.saveBroadcastStatus(ctx, status); err != nil {
		slog.Warn("notification email broadcast status update failed", "batch_id", status.BatchID, "error", err)
	}
}

func (s *NotificationEmailService) acquireBroadcastLock(ctx context.Context, batchID string) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, errors.New("notification email service is not configured")
	}
	if repo, ok := s.settingRepo.(notificationEmailAtomicSettingRepository); ok {
		return repo.SetIfAbsent(ctx, notificationEmailBroadcastActiveKey, batchID)
	}
	if _, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastActiveKey); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrSettingNotFound) {
		return false, err
	}
	if err := s.settingRepo.Set(ctx, notificationEmailBroadcastActiveKey, batchID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *NotificationEmailService) releaseBroadcastLockBestEffort(ctx context.Context, batchID string) {
	if s == nil || s.settingRepo == nil {
		return
	}
	active, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	if err != nil {
		if !errors.Is(err, ErrSettingNotFound) {
			slog.Warn("notification email broadcast active lock read failed", "batch_id", batchID, "error", err)
		}
		return
	}
	if strings.TrimSpace(active) != strings.TrimSpace(batchID) {
		return
	}
	if err := s.settingRepo.Delete(ctx, notificationEmailBroadcastActiveKey); err != nil && !errors.Is(err, ErrSettingNotFound) {
		slog.Warn("notification email broadcast active lock release failed", "batch_id", batchID, "error", err)
	}
}

func (s *NotificationEmailService) releaseInterruptedActiveBroadcast(ctx context.Context, now time.Time) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("notification email service is not configured")
	}
	activeBatchID, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil
		}
		return err
	}
	activeBatchID = strings.TrimSpace(activeBatchID)
	if activeBatchID == "" {
		if err := s.settingRepo.Delete(ctx, notificationEmailBroadcastActiveKey); err != nil && !errors.Is(err, ErrSettingNotFound) {
			return err
		}
		return nil
	}
	status, err := s.getBroadcastStatusRaw(ctx, activeBatchID)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			s.releaseBroadcastLockBestEffort(ctx, activeBatchID)
			return nil
		}
		return err
	}
	status = s.interruptBroadcastStatusIfStale(ctx, status, now)
	if status.Status != "running" && status.Status != "canceling" {
		s.releaseBroadcastLockBestEffort(ctx, activeBatchID)
	}
	return nil
}

func (s *NotificationEmailService) interruptBroadcastStatusIfStale(ctx context.Context, status NotificationEmailBroadcastStatus, now time.Time) NotificationEmailBroadcastStatus {
	if status.Status != "running" && status.Status != "canceling" {
		return status
	}
	updatedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(status.UpdatedAt))
	if err != nil {
		updatedAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(status.UpdatedAt))
	}
	if err != nil || updatedAt.IsZero() {
		return status
	}
	if now.UTC().Sub(updatedAt.UTC()) <= notificationEmailBroadcastStaleAfter {
		return status
	}
	status.Status = "interrupted"
	status.CompletedAt = now.UTC().Format(time.RFC3339)
	status.LastError = "broadcast interrupted because the worker stopped updating status"
	s.saveBroadcastStatusBestEffort(ctx, status)
	s.releaseBroadcastLockBestEffort(ctx, status.BatchID)
	return status
}

func (s *NotificationEmailService) getBroadcastStatusRaw(ctx context.Context, batchID string) (NotificationEmailBroadcastStatus, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailBroadcastStatus{}, errors.New("notification email service is not configured")
	}
	raw, err := s.settingRepo.GetValue(ctx, notificationEmailBroadcastKey(batchID))
	if err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	var status NotificationEmailBroadcastStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	return status, nil
}

func normalizeNotificationEmailBroadcastRPM(rpm int) int {
	if rpm <= 0 {
		return notificationEmailBroadcastDefaultRPM
	}
	if rpm > notificationEmailBroadcastMaxRPM {
		return notificationEmailBroadcastMaxRPM
	}
	return rpm
}

func notificationEmailBroadcastEstimateSeconds(count, rpm int) int {
	if count <= 1 {
		return 0
	}
	rpm = normalizeNotificationEmailBroadcastRPM(rpm)
	return int((time.Minute / time.Duration(rpm) * time.Duration(count-1)).Seconds())
}

func notificationEmailBroadcastInitialRecipientStates(recipients []notificationEmailBroadcastRecipient) []notificationEmailBroadcastRecipientState {
	states := make([]notificationEmailBroadcastRecipientState, 0, len(recipients))
	for _, recipient := range recipients {
		email := strings.TrimSpace(recipient.Email)
		if email == "" {
			continue
		}
		name := strings.TrimSpace(recipient.Name)
		if name == "" {
			name = emailRecipientName(email)
		}
		states = append(states, notificationEmailBroadcastRecipientState{
			UserID: recipient.UserID,
			Email:  email,
			Name:   name,
			Status: "pending",
		})
	}
	return states
}

func notificationEmailBroadcastStateByEmail(states []notificationEmailBroadcastRecipientState) map[string]*notificationEmailBroadcastRecipientState {
	out := make(map[string]*notificationEmailBroadcastRecipientState, len(states))
	for i := range states {
		key := strings.ToLower(strings.TrimSpace(states[i].Email))
		if key == "" {
			continue
		}
		out[key] = &states[i]
	}
	return out
}

func notificationEmailBroadcastRetryRecipients(states []notificationEmailBroadcastRecipientState, mode string) []notificationEmailBroadcastRecipient {
	recipients := make([]notificationEmailBroadcastRecipient, 0, len(states))
	for _, state := range states {
		status := strings.ToLower(strings.TrimSpace(state.Status))
		switch mode {
		case "failed":
			if status != "failed" {
				continue
			}
		default:
			if status == "sent" || status == "skipped" {
				continue
			}
		}
		recipients = append(recipients, notificationEmailBroadcastRecipient{
			UserID: state.UserID,
			Email:  state.Email,
			Name:   state.Name,
		})
	}
	return recipients
}

func notificationEmailBroadcastApplyCounts(status *NotificationEmailBroadcastStatus, states []notificationEmailBroadcastRecipientState) {
	if status == nil {
		return
	}
	status.TargetCount = len(states)
	status.SentCount = 0
	status.SkippedCount = 0
	status.UnsubscribedCount = 0
	status.FailureCount = 0
	for _, state := range states {
		switch strings.ToLower(strings.TrimSpace(state.Status)) {
		case "sent":
			status.SentCount++
		case "skipped":
			status.SkippedCount++
			if strings.EqualFold(strings.TrimSpace(state.Error), "unsubscribed") {
				status.UnsubscribedCount++
			}
		case "failed":
			status.FailureCount++
		}
	}
}

func notificationEmailBroadcastSortTime(status NotificationEmailBroadcastStatus) time.Time {
	for _, raw := range []string{status.UpdatedAt, status.StartedAt, status.CompletedAt} {
		if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
			return ts
		}
		if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func notificationEmailBroadcastBatchID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "broadcast_" + hex.EncodeToString(buf), nil
}

func notificationEmailBroadcastKey(batchID string) string {
	return notificationEmailBroadcastKeyPrefix + safeNotificationEmailKeyPart(batchID)
}

func notificationEmailBroadcastPayloadKey(batchID string) string {
	return notificationEmailBroadcastKeyPrefix + "payload:" + safeNotificationEmailKeyPart(batchID)
}

func notificationEmailBroadcastRecipientsKey(batchID string) string {
	return notificationEmailBroadcastKeyPrefix + "recipients:" + safeNotificationEmailKeyPart(batchID)
}

func notificationEmailBroadcastCancelKey(batchID string) string {
	return notificationEmailBroadcastKeyPrefix + "cancel:" + safeNotificationEmailKeyPart(batchID)
}

func notificationEmailBroadcastActionHTML(label, rawURL string) string {
	label = strings.TrimSpace(label)
	rawURL = strings.TrimSpace(rawURL)
	if label == "" || rawURL == "" || !isSafeNotificationEmailURL(rawURL) {
		return ""
	}
	return fmt.Sprintf(`<p><a class="button" href="%s">%s</a></p>`, html.EscapeString(rawURL), html.EscapeString(label))
}

func (s *NotificationEmailService) IsUnsubscribed(ctx context.Context, email, event string) (bool, error) {
	info, normalizedEvent, err := s.eventInfo(event)
	if err != nil {
		return false, err
	}
	if !info.Optional {
		return false, nil
	}
	for _, key := range []string{notificationEmailPreferenceKey(normalizedEvent, email), legacyNotificationEmailPreferenceKey(normalizedEvent, email)} {
		if strings.TrimSpace(key) == "" {
			continue
		}
		value, err := s.settingRepo.GetValue(ctx, key)
		if err == nil {
			return strings.EqualFold(strings.TrimSpace(value), "unsubscribed"), nil
		}
		if !errors.Is(err, ErrSettingNotFound) {
			return false, err
		}
	}
	return false, nil
}

func (s *NotificationEmailService) Unsubscribe(ctx context.Context, token string) (NotificationEmailUnsubscribeResult, error) {
	claims, err := s.parseUnsubscribeToken(ctx, token)
	if err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	info, normalizedEvent, err := s.eventInfo(claims.Event)
	if err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	if !info.Optional {
		return NotificationEmailUnsubscribeResult{}, fmt.Errorf("%s is transactional and cannot be unsubscribed", normalizedEvent)
	}
	if err := s.settingRepo.Set(ctx, notificationEmailPreferenceKey(normalizedEvent, claims.Email), "unsubscribed"); err != nil {
		return NotificationEmailUnsubscribeResult{}, err
	}
	return NotificationEmailUnsubscribeResult{Event: normalizedEvent, EventLabel: info.Label, Email: claims.Email, Done: true}, nil
}

func (s *NotificationEmailService) eventInfo(event string) (NotificationEmailEventInfo, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(event))
	info, ok := notificationEmailEventDefinitions[normalized]
	if !ok {
		return NotificationEmailEventInfo{}, "", fmt.Errorf("unsupported email template event: %s", event)
	}
	return info, normalized, nil
}

func (s *NotificationEmailService) sampleVariables(ctx context.Context, event, locale string) map[string]string {
	info := notificationEmailEventDefinitions[event]
	variables := make(map[string]string, len(info.Placeholders))
	for key, value := range notificationEmailSampleVariables(locale) {
		variables[key] = value
	}
	variables["site_name"] = s.siteName(ctx)
	baseURL := s.unsubscribePageBaseURL(ctx)
	variables["reset_url"] = joinNotificationEmailURL(baseURL, "/reset-password?token=preview")
	variables["recharge_url"] = joinNotificationEmailURL(baseURL, "/payment")
	variables["action_url"] = joinNotificationEmailURL(baseURL, "/notice")
	variables["action_html"] = notificationEmailBroadcastActionHTML(variables["action_label"], variables["action_url"])
	variables["monitor_url"] = joinNotificationEmailURL(baseURL, "/admin/channels/monitor")
	if info.Optional {
		variables["unsubscribe_url"] = joinNotificationEmailURL(baseURL, notificationEmailUnsubscribePagePath+"?token=preview")
	}
	return variables
}

func (s *NotificationEmailService) runtimeVariables(ctx context.Context, event, locale string, input NotificationEmailSendInput) (map[string]string, map[string]string, error) {
	variables := s.sampleVariables(ctx, event, locale)
	for key, value := range input.Variables {
		variables[key] = value
	}
	variables["site_name"] = s.siteName(ctx)
	variables["recipient_email"] = input.RecipientEmail
	if strings.TrimSpace(input.RecipientName) != "" {
		variables["recipient_name"] = input.RecipientName
	}
	headers := map[string]string(nil)
	if notificationEmailEventDefinitions[event].Optional {
		token, err := s.createUnsubscribeToken(ctx, input.RecipientEmail, event)
		if err != nil {
			return nil, nil, fmt.Errorf("generate unsubscribe token: %w", err)
		}
		variables["unsubscribe_url"] = s.buildUnsubscribePageURL(ctx, token)
		headers = s.buildUnsubscribeHeaders(ctx, token)
	}
	return variables, headers, nil
}

func (s *NotificationEmailService) siteName(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return defaultSiteName
	}
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || strings.TrimSpace(name) == "" {
		return defaultSiteName
	}
	return strings.TrimSpace(name)
}

func (s *NotificationEmailService) configuredBaseURL(ctx context.Context, keys ...string) string {
	if s == nil || s.settingRepo == nil {
		return ""
	}
	for _, key := range keys {
		value, err := s.settingRepo.GetValue(ctx, key)
		if err != nil {
			continue
		}
		trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
		if isHTTPNotificationEmailURL(trimmed) {
			return trimmed
		}
	}
	return ""
}

func (s *NotificationEmailService) unsubscribePageBaseURL(ctx context.Context) string {
	return s.configuredBaseURL(ctx, SettingKeyFrontendURL, SettingKeyAPIBaseURL)
}

func (s *NotificationEmailService) unsubscribeAPIBaseURL(ctx context.Context) string {
	return s.configuredBaseURL(ctx, SettingKeyAPIBaseURL)
}

func (s *NotificationEmailService) buildUnsubscribePageURL(ctx context.Context, token string) string {
	return joinNotificationEmailURL(s.unsubscribePageBaseURL(ctx), notificationEmailUnsubscribePagePath+"?token="+url.QueryEscape(token))
}

func (s *NotificationEmailService) buildUnsubscribeAPIURL(ctx context.Context, token string) string {
	return joinNotificationEmailURL(s.unsubscribeAPIBaseURL(ctx), notificationEmailUnsubscribeAPIPath+"?token="+url.QueryEscape(token))
}

func (s *NotificationEmailService) buildUnsubscribeHeaders(ctx context.Context, token string) map[string]string {
	apiURL := s.buildUnsubscribeAPIURL(ctx, token)
	if strings.TrimSpace(apiURL) == "" {
		return nil
	}
	return map[string]string{
		"List-Unsubscribe":      "<" + apiURL + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
}

func (s *NotificationEmailService) createUnsubscribeToken(ctx context.Context, email, event string) (string, error) {
	secret, err := s.unsubscribeSecret(ctx)
	if err != nil {
		return "", err
	}
	claims := notificationEmailUnsubscribeClaims{Email: strings.TrimSpace(email), Event: event, Exp: s.nowUTC().Add(notificationEmailUnsubscribeTTL).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signature := signNotificationEmailToken(secret, encodedPayload)
	return encodedPayload + "." + signature, nil
}

func (s *NotificationEmailService) parseUnsubscribeToken(ctx context.Context, token string) (notificationEmailUnsubscribeClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token")
	}
	secret, err := s.unsubscribeSecret(ctx)
	if err != nil {
		return notificationEmailUnsubscribeClaims{}, err
	}
	expected := signNotificationEmailToken(secret, parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token payload")
	}
	var claims notificationEmailUnsubscribeClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token payload")
	}
	if strings.TrimSpace(claims.Email) == "" || strings.TrimSpace(claims.Event) == "" {
		return notificationEmailUnsubscribeClaims{}, errors.New("invalid unsubscribe token claims")
	}
	if claims.Exp <= s.nowUTC().Unix() {
		return notificationEmailUnsubscribeClaims{}, errors.New("unsubscribe token expired")
	}
	return claims, nil
}

func (s *NotificationEmailService) unsubscribeSecret(ctx context.Context) (string, error) {
	secret, err := s.settingRepo.GetValue(ctx, notificationEmailUnsubscribeSecretKey)
	if err == nil && strings.TrimSpace(secret) != "" {
		return strings.TrimSpace(secret), nil
	}
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	if err := s.settingRepo.Set(ctx, notificationEmailUnsubscribeSecretKey, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *NotificationEmailService) deliveryExists(ctx context.Context, keys ...string) (bool, error) {
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		_, err := s.settingRepo.GetValue(ctx, key)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, ErrSettingNotFound) {
			return false, err
		}
	}
	return false, nil
}

func validateNotificationEmailTemplate(event, subject, htmlBody string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return errors.New("email subject cannot be empty")
	}
	if len([]rune(subject)) > notificationEmailMaxSubjectLength {
		return fmt.Errorf("email subject cannot exceed %d characters", notificationEmailMaxSubjectLength)
	}
	if strings.TrimSpace(htmlBody) == "" {
		return errors.New("email html cannot be empty")
	}
	if len([]byte(htmlBody)) > notificationEmailMaxHTMLLength {
		return fmt.Errorf("email html cannot exceed %d bytes", notificationEmailMaxHTMLLength)
	}
	allowed := notificationEmailAllowedPlaceholderSet(event)
	for _, placeholder := range notificationEmailPlaceholdersIn(subject + "\n" + htmlBody) {
		if _, ok := allowed[placeholder]; !ok {
			return fmt.Errorf("unsupported placeholder {{%s}} for event %s", placeholder, event)
		}
	}
	return nil
}

func renderNotificationEmail(event, subject, htmlBody string, variables map[string]string, rawHTMLVariables map[string]string) (NotificationEmailPreview, error) {
	if err := validateNotificationEmailTemplate(event, subject, htmlBody); err != nil {
		return NotificationEmailPreview{}, err
	}
	renderedSubject, err := renderNotificationEmailString(event, subject, variables, nil, false)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	renderedHTML, err := renderNotificationEmailString(event, htmlBody, variables, rawHTMLVariables, true)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	return NotificationEmailPreview{Subject: sanitizeEmailHeader(renderedSubject), HTML: renderedHTML}, nil
}

func renderNotificationEmailString(event, raw string, variables map[string]string, rawHTMLVariables map[string]string, escapeHTML bool) (string, error) {
	allowed := notificationEmailAllowedPlaceholderSet(event)
	var renderErr error
	rendered := notificationEmailPlaceholderPattern.ReplaceAllStringFunc(raw, func(match string) string {
		if renderErr != nil {
			return ""
		}
		parts := notificationEmailPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		name := parts[1]
		if _, ok := allowed[name]; !ok {
			renderErr = fmt.Errorf("unsupported placeholder {{%s}} for event %s", name, event)
			return ""
		}
		value := variables[name]
		if escapeHTML && notificationEmailRawHTMLAllowed(event, name) {
			if rawHTMLVariables != nil {
				if rawValue, ok := rawHTMLVariables[name]; ok {
					return rawValue
				}
			}
		}
		if strings.HasSuffix(name, "_url") && !isSafeNotificationEmailURL(value) {
			value = ""
		}
		if escapeHTML {
			return html.EscapeString(value)
		}
		return sanitizeEmailHeader(value)
	})
	if renderErr != nil {
		return "", renderErr
	}
	return rendered, nil
}

func notificationEmailRawHTMLAllowed(event, placeholder string) bool {
	return (event == NotificationEmailEventOpsScheduledReport && placeholder == "report_html") ||
		(event == NotificationEmailEventAdminBroadcast && (placeholder == "message_html" || placeholder == "action_html"))
}

func previewRawHTMLVariables(event string, variables map[string]string) map[string]string {
	switch event {
	case NotificationEmailEventAdminBroadcast:
		return map[string]string{
			"message_html": variables["message_html"],
			"action_html":  notificationEmailBroadcastActionHTML(variables["action_label"], variables["action_url"]),
		}
	case NotificationEmailEventOpsScheduledReport:
		return map[string]string{"report_html": variables["report_html"]}
	default:
		return nil
	}
}

func notificationEmailAllowedPlaceholderSet(event string) map[string]struct{} {
	info := notificationEmailEventDefinitions[event]
	allowed := make(map[string]struct{}, len(info.Placeholders))
	for _, placeholder := range info.Placeholders {
		allowed[placeholder] = struct{}{}
	}
	return allowed
}

func notificationEmailPlaceholdersIn(raw string) []string {
	matches := notificationEmailPlaceholderPattern.FindAllStringSubmatch(raw, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		if _, exists := seen[match[1]]; exists {
			continue
		}
		seen[match[1]] = struct{}{}
		out = append(out, match[1])
	}
	return out
}

func normalizeNotificationLocale(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return notificationEmailDefaultLocale
	}
	for _, part := range strings.Split(trimmed, ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		if strings.HasPrefix(tag, "zh") || tag == "cn" {
			return notificationEmailLocaleChinese
		}
		if strings.HasPrefix(tag, "en") {
			return notificationEmailDefaultLocale
		}
	}
	return notificationEmailDefaultLocale
}

func notificationEmailTemplateKey(event, locale string) string {
	return notificationEmailTemplateKeyPrefix + event + ":" + locale
}

func notificationEmailPreferenceKey(event, email string) string {
	if strings.TrimSpace(event) == "" || strings.TrimSpace(email) == "" {
		return ""
	}
	identity := strings.TrimSpace(event) + "\x00" + strings.ToLower(strings.TrimSpace(email))
	return notificationEmailPreferenceKeyPrefix + "v2:" + notificationEmailHash(identity)
}

func legacyNotificationEmailPreferenceKey(event, email string) string {
	return notificationEmailPreferenceKeyPrefix + event + ":" + notificationEmailHash(email)
}

func notificationEmailDeliveryKey(event, sourceType, sourceID, recipient, reminderKey string) string {
	if strings.TrimSpace(sourceType) == "" || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(recipient) == "" {
		return ""
	}
	identity := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(event)),
		safeNotificationEmailKeyPart(sourceType),
		safeNotificationEmailKeyPart(sourceID),
		strings.ToLower(strings.TrimSpace(recipient)),
		safeNotificationEmailKeyPart(reminderKey),
	}, "\x00")
	return notificationEmailDeliveryKeyPrefix + "v2:" + notificationEmailHash(identity)
}

func legacyNotificationEmailDeliveryKey(event, sourceType, sourceID, recipient, reminderKey string) string {
	if strings.TrimSpace(sourceType) == "" || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(recipient) == "" {
		return ""
	}
	parts := []string{notificationEmailDeliveryKeyPrefix, event, ":", safeNotificationEmailKeyPart(sourceType), ":", safeNotificationEmailKeyPart(sourceID), ":", notificationEmailHash(recipient)}
	if strings.TrimSpace(reminderKey) != "" {
		parts = append(parts, ":", safeNotificationEmailKeyPart(reminderKey))
	}
	return strings.Join(parts, "")
}

func notificationEmailHash(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func safeNotificationEmailKeyPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			_, _ = builder.WriteRune(r)
		} else {
			_, _ = builder.WriteRune('_')
		}
	}
	return builder.String()
}

func signNotificationEmailToken(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isSafeNotificationEmailURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		return scheme == "http" || scheme == "https" || scheme == "mailto"
	}
	return strings.HasPrefix(trimmed, "/")
}

func isHTTPNotificationEmailURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func joinNotificationEmailURL(baseURL, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	if parsed, err := url.Parse(path); err == nil && parsed.IsAbs() {
		return path
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		if strings.HasPrefix(path, "/") {
			return path
		}
		return "/" + path
	}
	if strings.HasPrefix(path, "/") {
		return baseURL + path
	}
	return baseURL + "/" + path
}

func notificationEmailSampleVariables(locale string) map[string]string {
	if normalizeNotificationLocale(locale) == notificationEmailLocaleChinese {
		return map[string]string{
			"site_name":           defaultSiteName,
			"recipient_name":      "张三",
			"recipient_email":     "user@example.com",
			"verification_code":   "123456",
			"expires_in_minutes":  "15",
			"reset_url":           "/reset-password?token=preview",
			"subscription_group":  "Claude Pro",
			"subscription_days":   "30",
			"expiry_time":         "2026-06-18 12:00",
			"days_remaining":      "3",
			"current_balance":     "12.34",
			"threshold":           "20.00",
			"recharge_url":        "/payment",
			"recharge_amount":     "50.00",
			"order_id":            "1024",
			"unsubscribe_url":     "/email-unsubscribe?token=preview",
			"account_id":          "1001",
			"account_name":        "openai-main",
			"platform":            "openai",
			"quota_dimension":     "每日额度",
			"quota_used":          "80.00",
			"quota_limit":         "100.00",
			"quota_remaining":     "20.00",
			"quota_threshold":     "20%",
			"triggered_at":        "2026-05-20 12:00:00",
			"group_name":          "默认分组",
			"moderation_category": "violence",
			"moderation_score":    "0.982",
			"violation_count":     "2",
			"ban_threshold":       "3",
			"rule_name":           "错误率过高",
			"severity":            "critical",
			"alert_status":        "firing",
			"metric_type":         "error_rate",
			"operator":            ">=",
			"metric_value":        "12.50",
			"threshold_value":     "10.00",
			"alert_description":   "最近 10 分钟错误率超过阈值",
			"report_name":         "日报",
			"report_type":         "daily_summary",
			"report_start_time":   "2026-05-19 12:00",
			"report_end_time":     "2026-05-20 12:00",
			"report_html":         "<h2>日报</h2><p>请求量：1024</p>",
			"message_title":       "重要服务通知",
			"message_html":        "<p>这是一封由管理员发送给用户的重要邮件通知。</p>",
			"action_label":        "查看详情",
			"action_url":          "/notice",
			"action_html":         `<p><a class="button" href="/notice">查看详情</a></p>`,
			"monitor_name":        "主线路",
			"provider":            "openai",
			"model":               "gpt-5.4",
			"old_status":          "failed",
			"new_status":          "operational",
			"latency_ms":          "123",
			"message":             "检测恢复正常",
			"monitor_url":         "/admin/channels/monitor",
		}
	}
	return map[string]string{
		"site_name":           defaultSiteName,
		"recipient_name":      "Alex",
		"recipient_email":     "user@example.com",
		"verification_code":   "123456",
		"expires_in_minutes":  "15",
		"reset_url":           "/reset-password?token=preview",
		"subscription_group":  "Claude Pro",
		"subscription_days":   "30",
		"expiry_time":         "2026-06-18 12:00",
		"days_remaining":      "3",
		"current_balance":     "12.34",
		"threshold":           "20.00",
		"recharge_url":        "/payment",
		"recharge_amount":     "50.00",
		"order_id":            "1024",
		"unsubscribe_url":     "/email-unsubscribe?token=preview",
		"account_id":          "1001",
		"account_name":        "openai-main",
		"platform":            "openai",
		"quota_dimension":     "Daily quota",
		"quota_used":          "80.00",
		"quota_limit":         "100.00",
		"quota_remaining":     "20.00",
		"quota_threshold":     "20%",
		"triggered_at":        "2026-05-20 12:00:00",
		"group_name":          "Default group",
		"moderation_category": "violence",
		"moderation_score":    "0.982",
		"violation_count":     "2",
		"ban_threshold":       "3",
		"rule_name":           "High error rate",
		"severity":            "critical",
		"alert_status":        "firing",
		"metric_type":         "error_rate",
		"operator":            ">=",
		"metric_value":        "12.50",
		"threshold_value":     "10.00",
		"alert_description":   "Error rate exceeded threshold in the last 10 minutes.",
		"report_name":         "Daily summary",
		"report_type":         "daily_summary",
		"report_start_time":   "2026-05-19 12:00",
		"report_end_time":     "2026-05-20 12:00",
		"report_html":         "<h2>Daily summary</h2><p>Requests: 1024</p>",
		"message_title":       "Important service notice",
		"message_html":        "<p>This is an important email notification sent by an administrator.</p>",
		"action_label":        "View details",
		"action_url":          "/notice",
		"action_html":         `<p><a class="button" href="/notice">View details</a></p>`,
		"monitor_name":        "Primary route",
		"provider":            "openai",
		"model":               "gpt-5.4",
		"old_status":          "failed",
		"new_status":          "operational",
		"latency_ms":          "123",
		"message":             "Monitor recovered",
		"monitor_url":         "/admin/channels/monitor",
	}
}

var notificationEmailEventOrder = []string{
	NotificationEmailEventAuthVerifyCode,
	NotificationEmailEventAuthPasswordReset,
	NotificationEmailEventNotificationEmailVerifyCode,
	NotificationEmailEventSubscriptionPurchaseSuccess,
	NotificationEmailEventSubscriptionExpiryReminder,
	NotificationEmailEventBalanceLow,
	NotificationEmailEventBalanceRechargeSuccess,
	NotificationEmailEventAccountQuotaAlert,
	NotificationEmailEventContentModerationViolation,
	NotificationEmailEventContentModerationDisabled,
	NotificationEmailEventOpsAlert,
	NotificationEmailEventOpsScheduledReport,
	NotificationEmailEventAdminBroadcast,
	NotificationEmailEventChannelMonitorFailed,
	NotificationEmailEventChannelMonitorRecovered,
}

var notificationEmailEventDefinitions = map[string]NotificationEmailEventInfo{
	NotificationEmailEventAuthVerifyCode: {
		Event:        NotificationEmailEventAuthVerifyCode,
		Label:        "Email verification code",
		Description:  "Sent for registration, email binding, OAuth pending email, and TOTP verification flows.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "verification_code", "expires_in_minutes"),
	},
	NotificationEmailEventAuthPasswordReset: {
		Event:        NotificationEmailEventAuthPasswordReset,
		Label:        "Password reset",
		Description:  "Sent when a user requests a password reset link.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "reset_url", "expires_in_minutes"),
	},
	NotificationEmailEventNotificationEmailVerifyCode: {
		Event:        NotificationEmailEventNotificationEmailVerifyCode,
		Label:        "Notification email verification code",
		Description:  "Sent when a user verifies an extra notification email address.",
		Category:     "auth",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "verification_code", "expires_in_minutes"),
	},
	NotificationEmailEventSubscriptionPurchaseSuccess: {
		Event:        NotificationEmailEventSubscriptionPurchaseSuccess,
		Label:        "Subscription purchase success",
		Description:  "Sent after a subscription purchase is fulfilled.",
		Category:     "subscription",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "subscription_group", "subscription_days", "expiry_time", "order_id"),
	},
	NotificationEmailEventSubscriptionExpiryReminder: {
		Event:        NotificationEmailEventSubscriptionExpiryReminder,
		Label:        "Subscription expiry reminder",
		Description:  "Optional reminder sent before an active subscription expires.",
		Category:     "subscription",
		Optional:     true,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "subscription_group", "expiry_time", "days_remaining", "unsubscribe_url"),
	},
	NotificationEmailEventBalanceLow: {
		Event:        NotificationEmailEventBalanceLow,
		Label:        "Low balance alert",
		Description:  "Optional alert sent when balance crosses the configured low-balance threshold.",
		Category:     "billing",
		Optional:     true,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "current_balance", "threshold", "recharge_url", "unsubscribe_url"),
	},
	NotificationEmailEventBalanceRechargeSuccess: {
		Event:        NotificationEmailEventBalanceRechargeSuccess,
		Label:        "Balance recharge success",
		Description:  "Sent after a balance recharge order is fulfilled.",
		Category:     "billing",
		Optional:     false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...), "recharge_amount", "current_balance", "order_id"),
	},
	NotificationEmailEventAccountQuotaAlert: {
		Event:       NotificationEmailEventAccountQuotaAlert,
		Label:       "Account quota alert",
		Description: "Sent to configured admin notification emails when an upstream account quota threshold is crossed.",
		Category:    "admin",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"account_id", "account_name", "platform", "quota_dimension", "quota_used", "quota_limit", "quota_remaining", "quota_threshold"),
	},
	NotificationEmailEventContentModerationViolation: {
		Event:       NotificationEmailEventContentModerationViolation,
		Label:       "Risk control violation notice",
		Description: "Sent to users when a request triggers content moderation/risk control rules.",
		Category:    "risk_control",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"triggered_at", "group_name", "moderation_category", "moderation_score", "violation_count", "ban_threshold"),
	},
	NotificationEmailEventContentModerationDisabled: {
		Event:       NotificationEmailEventContentModerationDisabled,
		Label:       "Risk control account disabled",
		Description: "Sent to users when content moderation automatically disables their account.",
		Category:    "risk_control",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"triggered_at", "group_name", "moderation_category", "moderation_score", "violation_count", "ban_threshold"),
	},
	NotificationEmailEventOpsAlert: {
		Event:       NotificationEmailEventOpsAlert,
		Label:       "Ops alert",
		Description: "Sent to configured operations recipients when an ops alert rule fires.",
		Category:    "ops",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"rule_name", "severity", "alert_status", "metric_type", "operator", "metric_value", "threshold_value", "triggered_at", "alert_description"),
	},
	NotificationEmailEventOpsScheduledReport: {
		Event:       NotificationEmailEventOpsScheduledReport,
		Label:       "Ops scheduled report",
		Description: "Sent to configured operations recipients for scheduled daily/weekly/error/account-health reports.",
		Category:    "ops",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"report_name", "report_type", "report_start_time", "report_end_time", "report_html"),
	},
	NotificationEmailEventAdminBroadcast: {
		Event:       NotificationEmailEventAdminBroadcast,
		Label:       "Admin email notification",
		Description: "Sent by administrators to selected user ranges for important email announcements.",
		Category:    "admin",
		Optional:    true,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"message_title", "message_html", "action_html", "action_label", "action_url", "unsubscribe_url"),
	},
	NotificationEmailEventChannelMonitorFailed: {
		Event:       NotificationEmailEventChannelMonitorFailed,
		Label:       "Channel monitor failure",
		Description: "Sent when a channel monitor model changes from healthy to failed/error.",
		Category:    "channel_monitor",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"monitor_name", "provider", "group_name", "model", "old_status", "new_status", "latency_ms", "message", "triggered_at", "monitor_url"),
	},
	NotificationEmailEventChannelMonitorRecovered: {
		Event:       NotificationEmailEventChannelMonitorRecovered,
		Label:       "Channel monitor recovered",
		Description: "Sent when a channel monitor model recovers from failed/error.",
		Category:    "channel_monitor",
		Optional:    false,
		Placeholders: append(append([]string{}, notificationEmailCommonPlaceholders...),
			"monitor_name", "provider", "group_name", "model", "old_status", "new_status", "latency_ms", "message", "triggered_at", "monitor_url"),
	},
}

var notificationEmailOfficialTemplates = map[string]map[string]notificationEmailOfficialTemplate{
	NotificationEmailEventAuthVerifyCode: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Email verification code",
			HTML: notificationEmailCard("#4f46e5", "Email verification code", `
<p>Hello {{recipient_name}},</p>
<p>Your verification code is:</p>
<p style="font-size: 32px; font-weight: 700; letter-spacing: 8px; text-align: center;">{{verification_code}}</p>
<p>This code expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p>If you did not request this code, please ignore this email.</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 邮箱验证码",
			HTML: notificationEmailCard("#4f46e5", "邮箱验证码", `
<p>{{recipient_name}}，您好：</p>
<p>您的验证码是：</p>
<p style="font-size: 32px; font-weight: 700; letter-spacing: 8px; text-align: center;">{{verification_code}}</p>
<p>验证码将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventAuthPasswordReset: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Password reset request",
			HTML: notificationEmailCard("#7c3aed", "Password reset", `
<p>Hello {{recipient_name}},</p>
<p>We received a request to reset your password. Click the button below to set a new password.</p>
<p><a class="button" href="{{reset_url}}">Reset password</a></p>
<p>This link expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p class="muted">If the button does not work, copy this link into your browser:<br>{{reset_url}}</p>
<p>If you did not request this, you can safely ignore this email.</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 密码重置请求",
			HTML: notificationEmailCard("#7c3aed", "密码重置", `
<p>{{recipient_name}}，您好：</p>
<p>我们收到了您的密码重置请求，请点击下方按钮设置新密码。</p>
<p><a class="button" href="{{reset_url}}">重置密码</a></p>
<p>此链接将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p class="muted">如果按钮无法点击，请复制以下链接到浏览器中打开：<br>{{reset_url}}</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventNotificationEmailVerifyCode: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Notification email verification code",
			HTML: notificationEmailCard("#0ea5e9", "Notification email verification", `
<p>Hello {{recipient_name}},</p>
<p>You are adding this address as an extra notification email.</p>
<p>Your verification code is:</p>
<p style="font-size: 32px; font-weight: 700; letter-spacing: 8px; text-align: center;">{{verification_code}}</p>
<p>This code expires in <strong>{{expires_in_minutes}}</strong> minutes.</p>
<p>If you did not request this code, please ignore this email.</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 通知邮箱验证码",
			HTML: notificationEmailCard("#0ea5e9", "通知邮箱验证", `
<p>{{recipient_name}}，您好：</p>
<p>您正在添加额外的通知邮箱，请输入以下验证码完成验证。</p>
<p style="font-size: 32px; font-weight: 700; letter-spacing: 8px; text-align: center;">{{verification_code}}</p>
<p>验证码将在 <strong>{{expires_in_minutes}}</strong> 分钟后失效。</p>
<p>如果不是您本人操作，请忽略此邮件。</p>`),
		},
	},
	NotificationEmailEventSubscriptionPurchaseSuccess: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Subscription purchase successful",
			HTML: notificationEmailCard("#2563eb", "Subscription activated", `
<p>Hello {{recipient_name}},</p>
<p>Your subscription for <strong>{{subscription_group}}</strong> has been activated for <strong>{{subscription_days}}</strong> days.</p>
<p>Expiry time: <strong>{{expiry_time}}</strong></p>
<p>Order ID: {{order_id}}</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 订阅购买成功",
			HTML: notificationEmailCard("#2563eb", "订阅已开通", `
<p>{{recipient_name}}，您好：</p>
<p>您的 <strong>{{subscription_group}}</strong> 订阅已成功开通，有效期 <strong>{{subscription_days}}</strong> 天。</p>
<p>到期时间：<strong>{{expiry_time}}</strong></p>
<p>订单号：{{order_id}}</p>`),
		},
	},
	NotificationEmailEventSubscriptionExpiryReminder: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Subscription expires in {{days_remaining}} day(s)",
			HTML: notificationEmailCard("#f97316", "Subscription expiry reminder", `
<p>Hello {{recipient_name}},</p>
<p>Your <strong>{{subscription_group}}</strong> subscription will expire in <strong>{{days_remaining}}</strong> day(s).</p>
<p>Expiry time: <strong>{{expiry_time}}</strong></p>
<p class="muted"><a href="{{unsubscribe_url}}">Unsubscribe from optional subscription reminders</a></p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 订阅将在 {{days_remaining}} 天后到期",
			HTML: notificationEmailCard("#f97316", "订阅到期提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您的 <strong>{{subscription_group}}</strong> 订阅将在 <strong>{{days_remaining}}</strong> 天后到期。</p>
<p>到期时间：<strong>{{expiry_time}}</strong></p>
<p class="muted"><a href="{{unsubscribe_url}}">退订此类订阅提醒</a></p>`),
		},
	},
	NotificationEmailEventBalanceLow: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Low balance alert",
			HTML: notificationEmailCard("#d97706", "Low balance alert", `
<p>Hello {{recipient_name}},</p>
<p>Your current balance is <strong>${{current_balance}}</strong>, below the configured alert threshold of <strong>${{threshold}}</strong>.</p>
<p>Please recharge in time to avoid service interruption.</p>
<p><a class="button" href="{{recharge_url}}">Recharge now</a></p>
<p class="muted"><a href="{{unsubscribe_url}}">Unsubscribe from optional balance alerts</a></p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 余额不足提醒",
			HTML: notificationEmailCard("#d97706", "余额不足提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您当前余额为 <strong>${{current_balance}}</strong>，已低于提醒阈值 <strong>${{threshold}}</strong>。</p>
<p>请及时充值以免服务中断。</p>
<p><a class="button" href="{{recharge_url}}">立即充值</a></p>
<p class="muted"><a href="{{unsubscribe_url}}">退订此类余额提醒</a></p>`),
		},
	},
	NotificationEmailEventBalanceRechargeSuccess: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Balance recharge successful",
			HTML: notificationEmailCard("#16a34a", "Recharge successful", `
<p>Hello {{recipient_name}},</p>
<p>Your balance recharge of <strong>${{recharge_amount}}</strong> has been completed.</p>
<p>Current balance: <strong>${{current_balance}}</strong></p>
<p>Order ID: {{order_id}}</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 余额充值成功",
			HTML: notificationEmailCard("#16a34a", "余额充值成功", `
<p>{{recipient_name}}，您好：</p>
<p>您的余额充值 <strong>${{recharge_amount}}</strong> 已完成。</p>
<p>当前余额：<strong>${{current_balance}}</strong></p>
			<p>订单号：{{order_id}}</p>`),
		},
	},
	NotificationEmailEventAccountQuotaAlert: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Account quota alert - {{account_name}}",
			HTML: notificationEmailCard("#dc2626", "Account quota alert", `
<p>The upstream account <strong>{{account_name}}</strong> has crossed its configured quota alert threshold.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Account ID</td><td>{{account_id}}</td></tr>
  <tr><td>Platform</td><td>{{platform}}</td></tr>
  <tr><td>Dimension</td><td>{{quota_dimension}}</td></tr>
  <tr><td>Used / Limit</td><td>{{quota_used}} / {{quota_limit}}</td></tr>
  <tr><td>Remaining</td><td>{{quota_remaining}}</td></tr>
  <tr><td>Threshold</td><td>{{quota_threshold}}</td></tr>
</table>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 账号限额告警 - {{account_name}}",
			HTML: notificationEmailCard("#dc2626", "账号限额告警", `
<p>上游账号 <strong>{{account_name}}</strong> 已触发配置的额度告警阈值。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>账号 ID</td><td>{{account_id}}</td></tr>
  <tr><td>平台</td><td>{{platform}}</td></tr>
  <tr><td>维度</td><td>{{quota_dimension}}</td></tr>
  <tr><td>已用 / 限额</td><td>{{quota_used}} / {{quota_limit}}</td></tr>
  <tr><td>剩余额度</td><td>{{quota_remaining}}</td></tr>
  <tr><td>告警阈值</td><td>{{quota_threshold}}</td></tr>
</table>`),
		},
	},
	NotificationEmailEventContentModerationViolation: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Risk control notice",
			HTML: notificationEmailCard("#ef4444", "Risk control notice", `
<p>Hello {{recipient_name}},</p>
<p>Your API request triggered the platform content moderation/risk-control policy.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Triggered at</td><td>{{triggered_at}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Category / Score</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>Violation count</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>Please review your request content to avoid future service interruptions.</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 账户风控提醒",
			HTML: notificationEmailCard("#ef4444", "账户风控提醒", `
<p>{{recipient_name}}，您好：</p>
<p>您的 API 请求触发了平台内容审核/风控策略。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>触发时间</td><td>{{triggered_at}}</td></tr>
  <tr><td>所属分组</td><td>{{group_name}}</td></tr>
  <tr><td>命中类别 / 分数</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>累计触发次数</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>请检查请求内容，避免后续服务受到影响。</p>`),
		},
	},
	NotificationEmailEventContentModerationDisabled: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Account disabled by risk control",
			HTML: notificationEmailCard("#b91c1c", "Account disabled", `
<p>Hello {{recipient_name}},</p>
<p>Your account has repeatedly triggered platform content moderation/risk-control rules and has been automatically disabled.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Disabled at</td><td>{{triggered_at}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Category / Score</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>Violation count</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>Please contact the administrator if you need to appeal or restore access.</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 账户已被禁用",
			HTML: notificationEmailCard("#b91c1c", "账户已被禁用", `
<p>{{recipient_name}}，您好：</p>
<p>您的账户在统计周期内多次触发平台内容审核/风控规则，系统已自动禁用该账户。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>禁用时间</td><td>{{triggered_at}}</td></tr>
  <tr><td>所属分组</td><td>{{group_name}}</td></tr>
  <tr><td>命中类别 / 分数</td><td>{{moderation_category}} / {{moderation_score}}</td></tr>
  <tr><td>累计触发次数</td><td>{{violation_count}} / {{ban_threshold}}</td></tr>
</table>
<p>如需申诉或恢复账号，请联系平台管理员处理。</p>`),
		},
	},
	NotificationEmailEventOpsAlert: {
		notificationEmailDefaultLocale: {
			Subject: "[Ops Alert][{{severity}}] {{rule_name}}",
			HTML: notificationEmailCard("#ea580c", "Ops alert", `
<p><strong>Rule</strong>: {{rule_name}}</p>
<p><strong>Severity</strong>: {{severity}}</p>
<p><strong>Status</strong>: {{alert_status}}</p>
<p><strong>Metric</strong>: {{metric_type}} {{operator}} {{metric_value}} (threshold {{threshold_value}})</p>
<p><strong>Fired at</strong>: {{triggered_at}}</p>
<p><strong>Description</strong>: {{alert_description}}</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[运维告警][{{severity}}] {{rule_name}}",
			HTML: notificationEmailCard("#ea580c", "运维告警", `
<p><strong>规则</strong>：{{rule_name}}</p>
<p><strong>严重级别</strong>：{{severity}}</p>
<p><strong>状态</strong>：{{alert_status}}</p>
<p><strong>指标</strong>：{{metric_type}} {{operator}} {{metric_value}}（阈值 {{threshold_value}}）</p>
<p><strong>触发时间</strong>：{{triggered_at}}</p>
<p><strong>说明</strong>：{{alert_description}}</p>`),
		},
	},
	NotificationEmailEventOpsScheduledReport: {
		notificationEmailDefaultLocale: {
			Subject: "[Ops Report] {{report_name}}",
			HTML: notificationEmailCard("#0891b2", "Ops report", `
<p><strong>Report</strong>: {{report_name}}</p>
<p><strong>Type</strong>: {{report_type}}</p>
<p><strong>Range</strong>: {{report_start_time}} - {{report_end_time}}</p>
<div>{{report_html}}</div>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[运维报表] {{report_name}}",
			HTML: notificationEmailCard("#0891b2", "运维报表", `
<p><strong>报表</strong>：{{report_name}}</p>
<p><strong>类型</strong>：{{report_type}}</p>
<p><strong>时间范围</strong>：{{report_start_time}} - {{report_end_time}}</p>
<div>{{report_html}}</div>`),
		},
	},
	NotificationEmailEventAdminBroadcast: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] {{message_title}}",
			HTML: notificationEmailCard("#2563eb", "{{message_title}}", `
<p>Hello {{recipient_name}},</p>
<div>{{message_html}}</div>
<div>{{action_html}}</div>
<p class="muted">You received this message because you have an account on {{site_name}}.</p>
<p class="muted"><a href="{{unsubscribe_url}}">Unsubscribe from optional email notices</a></p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] {{message_title}}",
			HTML: notificationEmailCard("#2563eb", "{{message_title}}", `
<p>{{recipient_name}}，您好：</p>
<div>{{message_html}}</div>
<div>{{action_html}}</div>
<p class="muted">您收到这封邮件，是因为您在 {{site_name}} 拥有账户。</p>
<p class="muted"><a href="{{unsubscribe_url}}">退订可选邮件通知</a></p>`),
		},
	},
	NotificationEmailEventChannelMonitorFailed: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Channel monitor failed: {{monitor_name}} / {{model}}",
			HTML: notificationEmailCard("#dc2626", "Channel monitor failed", `
<p>A monitored channel changed from healthy to failed/error.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Monitor</td><td>{{monitor_name}}</td></tr>
  <tr><td>Provider</td><td>{{provider}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Model</td><td>{{model}}</td></tr>
  <tr><td>Status</td><td>{{old_status}} → {{new_status}}</td></tr>
  <tr><td>Latency</td><td>{{latency_ms}} ms</td></tr>
  <tr><td>Checked at</td><td>{{triggered_at}}</td></tr>
</table>
<p><strong>Message</strong>: {{message}}</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 渠道监控故障：{{monitor_name}} / {{model}}",
			HTML: notificationEmailCard("#dc2626", "渠道监控故障", `
<p>一个渠道监控模型从健康状态变为失败/错误。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>监控</td><td>{{monitor_name}}</td></tr>
  <tr><td>平台</td><td>{{provider}}</td></tr>
  <tr><td>分组</td><td>{{group_name}}</td></tr>
  <tr><td>模型</td><td>{{model}}</td></tr>
  <tr><td>状态</td><td>{{old_status}} → {{new_status}}</td></tr>
  <tr><td>延迟</td><td>{{latency_ms}} ms</td></tr>
  <tr><td>检测时间</td><td>{{triggered_at}}</td></tr>
</table>
<p><strong>信息</strong>：{{message}}</p>`),
		},
	},
	NotificationEmailEventChannelMonitorRecovered: {
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] Channel monitor recovered: {{monitor_name}} / {{model}}",
			HTML: notificationEmailCard("#16a34a", "Channel monitor recovered", `
<p>A monitored channel recovered from failed/error.</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>Monitor</td><td>{{monitor_name}}</td></tr>
  <tr><td>Provider</td><td>{{provider}}</td></tr>
  <tr><td>Group</td><td>{{group_name}}</td></tr>
  <tr><td>Model</td><td>{{model}}</td></tr>
  <tr><td>Status</td><td>{{old_status}} → {{new_status}}</td></tr>
  <tr><td>Latency</td><td>{{latency_ms}} ms</td></tr>
  <tr><td>Checked at</td><td>{{triggered_at}}</td></tr>
</table>
<p><strong>Message</strong>: {{message}}</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] 渠道监控恢复：{{monitor_name}} / {{model}}",
			HTML: notificationEmailCard("#16a34a", "渠道监控恢复", `
<p>一个渠道监控模型已从失败/错误状态恢复。</p>
<table style="width:100%;border-collapse:collapse;">
  <tr><td>监控</td><td>{{monitor_name}}</td></tr>
  <tr><td>平台</td><td>{{provider}}</td></tr>
  <tr><td>分组</td><td>{{group_name}}</td></tr>
  <tr><td>模型</td><td>{{model}}</td></tr>
  <tr><td>状态</td><td>{{old_status}} → {{new_status}}</td></tr>
  <tr><td>延迟</td><td>{{latency_ms}} ms</td></tr>
  <tr><td>检测时间</td><td>{{triggered_at}}</td></tr>
</table>
<p><strong>信息</strong>：{{message}}</p>`),
		},
	},
}

func notificationEmailCard(accent, title, content string) string {
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    body { margin: 0; padding: 24px; background: #f4f4f5; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #18181b; }
    .container { max-width: 640px; margin: 0 auto; background: #ffffff; border-radius: 12px; overflow: hidden; box-shadow: 0 8px 30px rgba(15, 23, 42, 0.10); }
    .header { background: ` + accent + `; color: #ffffff; padding: 28px 32px; }
    .header h1 { margin: 0; font-size: 24px; line-height: 1.25; }
    .content { padding: 32px; font-size: 15px; line-height: 1.7; }
    .button { display: inline-block; margin-top: 12px; padding: 11px 18px; border-radius: 8px; background: ` + accent + `; color: #ffffff; text-decoration: none; font-weight: 600; }
    .muted { color: #71717a; font-size: 13px; }
    .footer { padding: 18px 32px; background: #fafafa; color: #a1a1aa; font-size: 12px; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header"><h1>` + title + `</h1></div>
    <div class="content">` + content + `</div>
    <div class="footer">This email was sent by {{site_name}}. Please do not reply directly.</div>
  </div>
</body>
</html>`
}
