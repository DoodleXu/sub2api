package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	NotificationEventChannelMonitorFailed    = "channel_monitor.failed"
	NotificationEventChannelMonitorRecovered = "channel_monitor.recovered"

	NotificationTransportEmail    = "email"
	NotificationTransportBark     = "bark"
	NotificationTransportTelegram = "telegram"

	notificationDefaultMinIntervalSeconds = 1800
	notificationHTTPTimeout               = 10 * time.Second
	notificationAsyncDispatchTimeout      = 30 * time.Second
	notificationMaxResponseBodyBytes      = 512
)

type NotificationConfig struct {
	Enabled    bool                         `json:"enabled"`
	Transports NotificationTransportConfigs `json:"transports"`
	Routes     map[string]NotificationRoute `json:"routes"`
	QuietHours NotificationQuietHoursConfig `json:"quiet_hours"`
}

type NotificationTransportConfigs struct {
	Email    NotificationEmailTransportConfig    `json:"email"`
	Bark     NotificationBarkTransportConfig     `json:"bark"`
	Telegram NotificationTelegramTransportConfig `json:"telegram"`
}

type NotificationEmailTransportConfig struct {
	Enabled    bool     `json:"enabled"`
	Recipients []string `json:"recipients"`
}

type NotificationBarkTransportConfig struct {
	Enabled              bool     `json:"enabled"`
	ServerURL            string   `json:"server_url"`
	DeviceKeys           []string `json:"device_keys,omitempty"`
	DeviceKeysConfigured bool     `json:"device_keys_configured"`
	ClearDeviceKeys      bool     `json:"clear_device_keys,omitempty"`
	Level                string   `json:"level"`
}

type NotificationTelegramTransportConfig struct {
	Enabled            bool     `json:"enabled"`
	BotToken           string   `json:"bot_token,omitempty"`
	BotTokenConfigured bool     `json:"bot_token_configured"`
	ClearBotToken      bool     `json:"clear_bot_token,omitempty"`
	ChatIDs            []string `json:"chat_ids"`
}

type NotificationRoute struct {
	Enabled            bool     `json:"enabled"`
	Transports         []string `json:"transports"`
	MinIntervalSeconds int      `json:"min_interval_seconds"`
}

type NotificationQuietHoursConfig struct {
	Enabled   bool   `json:"enabled"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Timezone  string `json:"timezone"`
}

type NotificationPayload struct {
	Title      string
	Content    string
	Event      string
	SourceType string
	SourceID   string
	Variables  map[string]string
}

type NotificationService struct {
	settingRepo              SettingRepository
	notificationEmailService *NotificationEmailService
	emailService             *EmailService
	encryptor                SecretEncryptor
	httpClient               *http.Client
	now                      func() time.Time
}

type notificationPartialDeliveryError struct {
	successCount int
	failureCount int
	err          error
}

func (e *notificationPartialDeliveryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("notification partially delivered: %d succeeded, %d failed: %v", e.successCount, e.failureCount, e.err)
}

func (e *notificationPartialDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func isPartialNotificationDelivery(err error) bool {
	var partial *notificationPartialDeliveryError
	return errors.As(err, &partial) && partial.successCount > 0
}

func NewNotificationService(settingRepo SettingRepository, notificationEmailService *NotificationEmailService, emailService *EmailService, encryptor SecretEncryptor) *NotificationService {
	return &NotificationService{
		settingRepo:              settingRepo,
		notificationEmailService: notificationEmailService,
		emailService:             emailService,
		encryptor:                encryptor,
		httpClient:               &http.Client{Timeout: notificationHTTPTimeout},
		now:                      func() time.Time { return time.Now().UTC() },
	}
}

func (s *NotificationService) GetConfig(ctx context.Context) (*NotificationConfig, error) {
	cfg, err := s.loadConfig(ctx, false)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *NotificationService) UpdateConfig(ctx context.Context, next *NotificationConfig) (*NotificationConfig, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("notification service is not configured")
	}
	if next == nil {
		return nil, errors.New("notification config is required")
	}
	current, err := s.loadConfig(ctx, true)
	if err != nil {
		return nil, err
	}
	merged := mergeNotificationConfig(current, next)
	if err := s.encryptNotificationSecrets(&merged); err != nil {
		return nil, err
	}
	normalizeNotificationConfig(&merged)
	if err := validateNotificationConfigForSave(&merged); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyNotificationConfig, string(raw)); err != nil {
		return nil, err
	}
	return s.GetConfig(ctx)
}

func (s *NotificationService) Dispatch(ctx context.Context, event string, payload NotificationPayload) {
	if s == nil {
		return
	}
	cfg, err := s.loadConfig(ctx, true)
	if err != nil {
		slog.Warn("notification: load config failed", "event", event, "error", err)
		return
	}
	if !cfg.Enabled {
		return
	}
	if s.isInQuietHours(cfg.QuietHours) {
		slog.Debug("notification: suppressed by quiet hours", "event", event, "timezone", cfg.QuietHours.Timezone)
		return
	}
	route, ok := cfg.Routes[event]
	if !ok || !route.Enabled {
		return
	}
	rateLimitKey := notificationRateLimitKey(event, payload.SourceType, payload.SourceID)
	if s.isRateLimited(ctx, event, rateLimitKey, route) {
		return
	}
	delivered := false
	for _, transport := range route.Transports {
		if !s.notificationTransportHasRecipient(transport, cfg) {
			continue
		}
		if err := s.send(ctx, strings.TrimSpace(transport), event, payload, cfg); err != nil {
			slog.Warn("notification: send failed", "event", event, "transport", transport, "error", err)
			if isPartialNotificationDelivery(err) {
				delivered = true
			}
			continue
		}
		delivered = true
	}
	if delivered {
		s.markRateLimited(ctx, event, rateLimitKey)
	}
}

func (s *NotificationService) DispatchAsync(ctx context.Context, event string, payload NotificationPayload) {
	if s == nil {
		return
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	go func() {
		dispatchCtx, cancel := context.WithTimeout(base, notificationAsyncDispatchTimeout)
		defer cancel()
		s.Dispatch(dispatchCtx, event, payload)
	}()
}

func (s *NotificationService) Test(ctx context.Context, transport string) error {
	cfg, err := s.loadConfig(ctx, true)
	if err != nil {
		return err
	}
	transport = strings.ToLower(strings.TrimSpace(transport))
	switch transport {
	case NotificationTransportEmail:
		if !cfg.Transports.Email.Enabled {
			return errors.New("email notification transport is disabled")
		}
	case NotificationTransportBark:
		if !cfg.Transports.Bark.Enabled {
			return errors.New("bark notification transport is disabled")
		}
	case NotificationTransportTelegram:
		if !cfg.Transports.Telegram.Enabled {
			return errors.New("telegram notification transport is disabled")
		}
	default:
		return fmt.Errorf("unsupported notification transport: %s", transport)
	}
	payload := NotificationPayload{
		Title:      "Sub2API notification test",
		Content:    "This is a test notification from Sub2API.",
		Event:      "notification.test",
		SourceType: "notification_test",
		SourceID:   strconv.FormatInt(s.now().UnixNano(), 10),
		Variables: map[string]string{
			"monitor_name":   "Test Monitor",
			"provider":       "openai",
			"group_name":     "default",
			"model":          "gpt-5.4",
			"old_status":     "failed",
			"new_status":     "operational",
			"latency_ms":     "123",
			"message":        "test message",
			"triggered_at":   s.now().Format(time.RFC3339),
			"monitor_url":    "",
			"recipient_name": "Admin",
		},
	}
	return s.send(ctx, transport, NotificationEventChannelMonitorRecovered, payload, cfg)
}

func (s *NotificationService) loadConfig(ctx context.Context, includeSecrets bool) (*NotificationConfig, error) {
	if s == nil || s.settingRepo == nil {
		cfg := defaultNotificationConfig()
		return &cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyNotificationConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			cfg := defaultNotificationConfig()
			return &cfg, nil
		}
		return nil, err
	}
	cfg := defaultNotificationConfig()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, fmt.Errorf("decode notification config: %w", err)
		}
	}
	if includeSecrets {
		if err := s.decryptNotificationSecrets(&cfg); err != nil {
			return nil, err
		}
		normalizeNotificationConfig(&cfg)
	} else {
		normalizeNotificationConfig(&cfg)
		maskNotificationSecrets(&cfg)
	}
	return &cfg, nil
}

func defaultNotificationConfig() NotificationConfig {
	return NotificationConfig{
		Enabled: false,
		Transports: NotificationTransportConfigs{
			Email: NotificationEmailTransportConfig{
				Enabled:    true,
				Recipients: []string{},
			},
			Bark: NotificationBarkTransportConfig{
				Enabled:   false,
				ServerURL: "https://api.day.app",
				Level:     "active",
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled: false,
				ChatIDs: []string{},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportEmail},
				MinIntervalSeconds: notificationDefaultMinIntervalSeconds,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled:            true,
				Transports:         []string{NotificationTransportEmail},
				MinIntervalSeconds: 300,
			},
		},
		QuietHours: NotificationQuietHoursConfig{
			Enabled:   false,
			StartTime: "22:00",
			EndTime:   "08:00",
			Timezone:  timezone.Name(),
		},
	}
}

func validateNotificationConfigForSave(cfg *NotificationConfig) error {
	if cfg == nil {
		return nil
	}
	if err := validateNotificationQuietHours(cfg.QuietHours); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	if err := validateNotificationEnabledTransports(cfg); err != nil {
		return err
	}
	hasEnabledRoute := false
	for event, route := range cfg.Routes {
		if !route.Enabled {
			continue
		}
		hasEnabledRoute = true
		if !notificationRouteHasDeliverableRecipient(cfg, route) {
			return fmt.Errorf("notification route %s requires at least one deliverable transport", event)
		}
	}
	if !hasEnabledRoute {
		return errors.New("notification route is required when notifications are enabled")
	}
	return nil
}

func validateNotificationEnabledTransports(cfg *NotificationConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.Transports.Email.Enabled && len(cfg.Transports.Email.Recipients) == 0 {
		return errors.New("email notification recipient is required when email transport is enabled")
	}
	if cfg.Transports.Bark.Enabled && len(cfg.Transports.Bark.DeviceKeys) == 0 {
		return errors.New("bark device key is required when bark transport is enabled")
	}
	if cfg.Transports.Telegram.Enabled {
		if strings.TrimSpace(cfg.Transports.Telegram.BotToken) == "" {
			return errors.New("telegram bot token is required when telegram transport is enabled")
		}
		if len(cfg.Transports.Telegram.ChatIDs) == 0 {
			return errors.New("telegram chat id is required when telegram transport is enabled")
		}
	}
	return nil
}

func notificationRouteHasDeliverableRecipient(cfg *NotificationConfig, route NotificationRoute) bool {
	if cfg == nil {
		return false
	}
	for _, transport := range route.Transports {
		switch transport {
		case NotificationTransportEmail:
			if cfg.Transports.Email.Enabled && len(cfg.Transports.Email.Recipients) > 0 {
				return true
			}
		case NotificationTransportBark:
			if cfg.Transports.Bark.Enabled && len(cfg.Transports.Bark.DeviceKeys) > 0 {
				return true
			}
		case NotificationTransportTelegram:
			if cfg.Transports.Telegram.Enabled && len(cfg.Transports.Telegram.ChatIDs) > 0 && strings.TrimSpace(cfg.Transports.Telegram.BotToken) != "" {
				return true
			}
		}
	}
	return false
}

func mergeNotificationConfig(current, next *NotificationConfig) NotificationConfig {
	cfg := defaultNotificationConfig()
	if current != nil {
		cfg = *current
	}
	cfg.Enabled = next.Enabled
	cfg.Transports.Email = next.Transports.Email
	cfg.Transports.Bark.Enabled = next.Transports.Bark.Enabled
	cfg.Transports.Bark.ServerURL = next.Transports.Bark.ServerURL
	cfg.Transports.Bark.Level = next.Transports.Bark.Level
	if next.Transports.Bark.ClearDeviceKeys {
		cfg.Transports.Bark.DeviceKeys = nil
	} else if len(next.Transports.Bark.DeviceKeys) > 0 {
		cfg.Transports.Bark.DeviceKeys = append([]string(nil), next.Transports.Bark.DeviceKeys...)
	}
	cfg.Transports.Telegram.Enabled = next.Transports.Telegram.Enabled
	cfg.Transports.Telegram.ChatIDs = append([]string(nil), next.Transports.Telegram.ChatIDs...)
	if next.Transports.Telegram.ClearBotToken {
		cfg.Transports.Telegram.BotToken = ""
	} else if strings.TrimSpace(next.Transports.Telegram.BotToken) != "" {
		cfg.Transports.Telegram.BotToken = strings.TrimSpace(next.Transports.Telegram.BotToken)
	}
	if next.Routes != nil {
		cfg.Routes = next.Routes
	}
	if next.QuietHours.Enabled || next.QuietHours.StartTime != "" || next.QuietHours.EndTime != "" || next.QuietHours.Timezone != "" {
		cfg.QuietHours = next.QuietHours
	}
	return cfg
}

func (s *NotificationService) notificationTransportHasRecipient(transport string, cfg *NotificationConfig) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case NotificationTransportEmail:
		return cfg.Transports.Email.Enabled && len(normalizeNotificationStrings(cfg.Transports.Email.Recipients)) > 0
	case NotificationTransportBark:
		return cfg.Transports.Bark.Enabled && len(normalizeNotificationStrings(cfg.Transports.Bark.DeviceKeys)) > 0
	case NotificationTransportTelegram:
		return cfg.Transports.Telegram.Enabled &&
			strings.TrimSpace(cfg.Transports.Telegram.BotToken) != "" &&
			len(normalizeNotificationStrings(cfg.Transports.Telegram.ChatIDs)) > 0
	default:
		return false
	}
}

func normalizeNotificationConfig(cfg *NotificationConfig) {
	if cfg == nil {
		return
	}
	cfg.Transports.Email.Recipients = normalizeNotificationStrings(cfg.Transports.Email.Recipients)
	cfg.Transports.Bark.DeviceKeys = normalizeNotificationStrings(cfg.Transports.Bark.DeviceKeys)
	cfg.Transports.Bark.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.Transports.Bark.ServerURL), "/")
	if cfg.Transports.Bark.ServerURL == "" {
		cfg.Transports.Bark.ServerURL = "https://api.day.app"
	}
	cfg.Transports.Bark.Level = normalizeBarkLevel(cfg.Transports.Bark.Level)
	cfg.Transports.Bark.DeviceKeysConfigured = len(cfg.Transports.Bark.DeviceKeys) > 0
	cfg.Transports.Bark.ClearDeviceKeys = false
	cfg.Transports.Telegram.BotToken = strings.TrimSpace(cfg.Transports.Telegram.BotToken)
	cfg.Transports.Telegram.BotTokenConfigured = cfg.Transports.Telegram.BotToken != ""
	cfg.Transports.Telegram.ClearBotToken = false
	cfg.Transports.Telegram.ChatIDs = normalizeNotificationStrings(cfg.Transports.Telegram.ChatIDs)
	if cfg.Routes == nil {
		cfg.Routes = map[string]NotificationRoute{}
	}
	cfg.QuietHours = normalizeNotificationQuietHours(cfg.QuietHours)
	defaults := defaultNotificationConfig()
	for event, route := range defaults.Routes {
		if _, ok := cfg.Routes[event]; !ok {
			cfg.Routes[event] = route
		}
	}
	for event, route := range cfg.Routes {
		route.Transports = normalizeNotificationTransports(route.Transports)
		if route.MinIntervalSeconds < 0 {
			route.MinIntervalSeconds = 0
		}
		cfg.Routes[event] = route
	}
}

func normalizeNotificationStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeNotificationQuietHours(cfg NotificationQuietHoursConfig) NotificationQuietHoursConfig {
	if strings.TrimSpace(cfg.StartTime) == "" {
		cfg.StartTime = "22:00"
	} else {
		cfg.StartTime = strings.TrimSpace(cfg.StartTime)
	}
	if strings.TrimSpace(cfg.EndTime) == "" {
		cfg.EndTime = "08:00"
	} else {
		cfg.EndTime = strings.TrimSpace(cfg.EndTime)
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = timezone.Name()
	} else {
		cfg.Timezone = strings.TrimSpace(cfg.Timezone)
	}
	return cfg
}

func validateNotificationQuietHours(cfg NotificationQuietHoursConfig) error {
	if _, err := parseNotificationQuietTime(cfg.StartTime); err != nil {
		return fmt.Errorf("invalid quiet hours start_time: %w", err)
	}
	if _, err := parseNotificationQuietTime(cfg.EndTime); err != nil {
		return fmt.Errorf("invalid quiet hours end_time: %w", err)
	}
	if _, err := time.LoadLocation(strings.TrimSpace(cfg.Timezone)); err != nil {
		return fmt.Errorf("invalid quiet hours timezone: %w", err)
	}
	return nil
}

func parseNotificationQuietTime(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) != len("15:04") || raw[2] != ':' {
		return 0, errors.New("must use HH:mm format")
	}
	for _, idx := range []int{0, 1, 3, 4} {
		if raw[idx] < '0' || raw[idx] > '9' {
			return 0, errors.New("must use HH:mm format")
		}
	}
	hour, _ := strconv.Atoi(raw[:2])
	minute, _ := strconv.Atoi(raw[3:])
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, errors.New("must be a valid 24-hour time")
	}
	return hour*60 + minute, nil
}

func (s *NotificationService) isInQuietHours(cfg NotificationQuietHoursConfig) bool {
	if !cfg.Enabled {
		return false
	}
	cfg = normalizeNotificationQuietHours(cfg)
	startMinute, err := parseNotificationQuietTime(cfg.StartTime)
	if err != nil {
		slog.Warn("notification: invalid quiet hours start_time", "start_time", cfg.StartTime, "error", err)
		return false
	}
	endMinute, err := parseNotificationQuietTime(cfg.EndTime)
	if err != nil {
		slog.Warn("notification: invalid quiet hours end_time", "end_time", cfg.EndTime, "error", err)
		return false
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		slog.Warn("notification: invalid quiet hours timezone", "timezone", cfg.Timezone, "error", err)
		return false
	}
	now := time.Now().In(loc)
	if s != nil && s.now != nil {
		now = s.now().In(loc)
	}
	currentMinute := now.Hour()*60 + now.Minute()
	if startMinute == endMinute {
		return true
	}
	if startMinute < endMinute {
		return currentMinute >= startMinute && currentMinute < endMinute
	}
	return currentMinute >= startMinute || currentMinute < endMinute
}

func normalizeNotificationTransports(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		transport := strings.ToLower(strings.TrimSpace(value))
		switch transport {
		case NotificationTransportEmail, NotificationTransportBark, NotificationTransportTelegram:
		default:
			continue
		}
		if _, ok := seen[transport]; ok {
			continue
		}
		seen[transport] = struct{}{}
		out = append(out, transport)
	}
	return out
}

func normalizeBarkLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "active", "passive":
		return strings.ToLower(strings.TrimSpace(raw))
	case "timesensitive", "time_sensitive", "time-sensitive":
		return "timeSensitive"
	case "critical":
		return "critical"
	default:
		return "active"
	}
}

func (s *NotificationService) encryptNotificationSecrets(cfg *NotificationConfig) error {
	if s == nil || s.encryptor == nil || cfg == nil {
		return nil
	}
	for i, key := range cfg.Transports.Bark.DeviceKeys {
		if strings.TrimSpace(key) == "" || strings.HasPrefix(key, "enc:") {
			continue
		}
		encrypted, err := s.encryptor.Encrypt(key)
		if err != nil {
			return fmt.Errorf("encrypt bark device key: %w", err)
		}
		cfg.Transports.Bark.DeviceKeys[i] = "enc:" + encrypted
	}
	token := strings.TrimSpace(cfg.Transports.Telegram.BotToken)
	if token != "" && !strings.HasPrefix(token, "enc:") {
		encrypted, err := s.encryptor.Encrypt(token)
		if err != nil {
			return fmt.Errorf("encrypt telegram bot token: %w", err)
		}
		cfg.Transports.Telegram.BotToken = "enc:" + encrypted
	}
	return nil
}

func (s *NotificationService) decryptNotificationSecrets(cfg *NotificationConfig) error {
	if s == nil || s.encryptor == nil || cfg == nil {
		return nil
	}
	for i, key := range cfg.Transports.Bark.DeviceKeys {
		if !strings.HasPrefix(key, "enc:") {
			continue
		}
		plain, err := s.encryptor.Decrypt(strings.TrimPrefix(key, "enc:"))
		if err != nil {
			return fmt.Errorf("decrypt bark device key: %w", err)
		}
		cfg.Transports.Bark.DeviceKeys[i] = plain
	}
	if strings.HasPrefix(cfg.Transports.Telegram.BotToken, "enc:") {
		plain, err := s.encryptor.Decrypt(strings.TrimPrefix(cfg.Transports.Telegram.BotToken, "enc:"))
		if err != nil {
			return fmt.Errorf("decrypt telegram bot token: %w", err)
		}
		cfg.Transports.Telegram.BotToken = plain
	}
	return nil
}

func maskNotificationSecrets(cfg *NotificationConfig) {
	if cfg == nil {
		return
	}
	cfg.Transports.Bark.DeviceKeysConfigured = len(cfg.Transports.Bark.DeviceKeys) > 0
	cfg.Transports.Bark.DeviceKeys = nil
	cfg.Transports.Telegram.BotTokenConfigured = strings.TrimSpace(cfg.Transports.Telegram.BotToken) != ""
	cfg.Transports.Telegram.BotToken = ""
}

func (s *NotificationService) send(ctx context.Context, transport, event string, payload NotificationPayload, cfg *NotificationConfig) error {
	switch transport {
	case NotificationTransportEmail:
		return s.sendEmail(ctx, event, payload, cfg.Transports.Email)
	case NotificationTransportBark:
		return s.sendBark(ctx, payload, cfg.Transports.Bark)
	case NotificationTransportTelegram:
		return s.sendTelegram(ctx, payload, cfg.Transports.Telegram)
	default:
		return fmt.Errorf("unsupported notification transport: %s", transport)
	}
}

func (s *NotificationService) sendEmail(ctx context.Context, event string, payload NotificationPayload, cfg NotificationEmailTransportConfig) error {
	if !cfg.Enabled {
		return errors.New("email notification transport is disabled")
	}
	recipients := normalizeNotificationStrings(cfg.Recipients)
	if len(recipients) == 0 {
		return errors.New("email notification recipient is not configured")
	}
	successCount := 0
	var failures []error
	for _, recipient := range recipients {
		if s.notificationEmailService != nil {
			err := s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
				Event:          event,
				RecipientEmail: recipient,
				RecipientName:  emailRecipientName(recipient),
				SourceType:     payload.SourceType,
				SourceID:       "",
				ReminderKey:    "",
				Variables:      payload.Variables,
			})
			if err == nil {
				successCount++
				continue
			}
			if !shouldFallbackNotificationEmail(err) {
				failures = append(failures, fmt.Errorf("recipient %s: %w", recipient, err))
				continue
			}
		}
		if s.emailService == nil {
			failures = append(failures, fmt.Errorf("recipient %s: email service is not configured", recipient))
			continue
		}
		if err := s.emailService.SendEmail(ctx, recipient, payload.Title, htmlEscapeLines(payload.Content)); err != nil {
			failures = append(failures, fmt.Errorf("recipient %s: %w", recipient, err))
			continue
		}
		successCount++
	}
	return notificationDeliveryResult(successCount, len(failures), failures)
}

func (s *NotificationService) sendBark(ctx context.Context, payload NotificationPayload, cfg NotificationBarkTransportConfig) error {
	if !cfg.Enabled {
		return errors.New("bark notification transport is disabled")
	}
	deviceKeys := normalizeNotificationStrings(cfg.DeviceKeys)
	if len(deviceKeys) == 0 {
		return errors.New("bark device key is not configured")
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	successCount := 0
	var failures []error
	for _, deviceKey := range deviceKeys {
		endpoint := strings.TrimRight(cfg.ServerURL, "/") + "/" + url.PathEscape(deviceKey)
		body := map[string]string{
			"title": payload.Title,
			"body":  payload.Content,
			"level": cfg.Level,
		}
		if monitorURL := strings.TrimSpace(payload.Variables["monitor_url"]); monitorURL != "" {
			body["url"] = monitorURL
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			failures = append(failures, sanitizeNotificationSecretInError(err, deviceKey, "bark device key"))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if err := doNotificationHTTP(client, req); err != nil {
			failures = append(failures, sanitizeNotificationSecretInError(err, deviceKey, "bark device key"))
			continue
		}
		successCount++
	}
	return notificationDeliveryResult(successCount, len(failures), failures)
}

func (s *NotificationService) sendTelegram(ctx context.Context, payload NotificationPayload, cfg NotificationTelegramTransportConfig) error {
	if !cfg.Enabled {
		return errors.New("telegram notification transport is disabled")
	}
	token := strings.TrimSpace(cfg.BotToken)
	if token == "" {
		return errors.New("telegram bot token is not configured")
	}
	chatIDs := normalizeNotificationStrings(cfg.ChatIDs)
	if len(chatIDs) == 0 {
		return errors.New("telegram chat id is not configured")
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
	text := payload.Title + "\n\n" + payload.Content
	successCount := 0
	var failures []error
	for _, chatID := range chatIDs {
		body := map[string]string{
			"chat_id": chatID,
			"text":    text,
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			failures = append(failures, sanitizeNotificationSecretInError(err, token, "telegram bot token"))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if err := doNotificationHTTP(client, req); err != nil {
			failures = append(failures, sanitizeNotificationSecretInError(err, token, "telegram bot token"))
			continue
		}
		successCount++
	}
	return notificationDeliveryResult(successCount, len(failures), failures)
}

func notificationDeliveryResult(successCount, failureCount int, failures []error) error {
	if failureCount == 0 {
		return nil
	}
	err := errors.Join(failures...)
	if successCount > 0 {
		return &notificationPartialDeliveryError{
			successCount: successCount,
			failureCount: failureCount,
			err:          err,
		}
	}
	return err
}

func sanitizeNotificationSecretInError(err error, secret, label string) error {
	if err == nil {
		return nil
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), secret, "[redacted "+label+"]")
	if escaped := url.PathEscape(secret); escaped != secret {
		msg = strings.ReplaceAll(msg, escaped, "[redacted "+label+"]")
	}
	if escaped := url.QueryEscape(secret); escaped != secret {
		msg = strings.ReplaceAll(msg, escaped, "[redacted "+label+"]")
	}
	return errors.New(msg)
}

func doNotificationHTTP(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, notificationMaxResponseBodyBytes))
	return fmt.Errorf("notification webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func htmlEscapeLines(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;", "\n", "<br>")
	return replacer.Replace(s)
}

func (s *NotificationService) isRateLimited(ctx context.Context, event, key string, route NotificationRoute) bool {
	if s == nil || s.settingRepo == nil || route.MinIntervalSeconds <= 0 || key == "" {
		return false
	}
	raw, err := s.settingRepo.GetValue(ctx, key)
	if err == nil {
		if sentAt, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
			if s.now().Sub(sentAt) < time.Duration(route.MinIntervalSeconds)*time.Second {
				return true
			}
		}
	} else if !errors.Is(err, ErrSettingNotFound) {
		slog.Warn("notification: rate limit lookup failed", "event", event, "error", err)
	}
	return false
}

func (s *NotificationService) markRateLimited(ctx context.Context, event, key string) {
	if s == nil || s.settingRepo == nil || key == "" {
		return
	}
	if err := s.settingRepo.Set(ctx, key, s.now().Format(time.RFC3339Nano)); err != nil {
		slog.Warn("notification: rate limit mark failed", "event", event, "error", err)
	}
}

func notificationRateLimitKey(event, sourceType, sourceID string) string {
	if strings.TrimSpace(event) == "" || strings.TrimSpace(sourceType) == "" || strings.TrimSpace(sourceID) == "" {
		return ""
	}
	identity := strings.Join([]string{
		strings.TrimSpace(event),
		strings.TrimSpace(sourceType),
		strings.TrimSpace(sourceID),
	}, "\x00")
	sum := sha256.Sum256([]byte(strings.ToLower(identity)))
	return "notification_delivery:v1:" + hex.EncodeToString(sum[:])
}
