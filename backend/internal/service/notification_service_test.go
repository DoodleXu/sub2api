package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type notificationPlainEncryptor struct{}

func (notificationPlainEncryptor) Encrypt(plaintext string) (string, error) {
	return "cipher:" + plaintext, nil
}

func (notificationPlainEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, "cipher:"), nil
}

type notificationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f notificationRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNotificationConfigMasksAndPreservesSecrets(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationService(repo, nil, nil, notificationPlainEncryptor{})

	updated, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  "https://bark.example",
				DeviceKeys: []string{"device-1"},
				Level:      "time_sensitive",
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled:  true,
				BotToken: "token-1",
				ChatIDs:  []string{"10001"},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark, NotificationTransportTelegram},
				MinIntervalSeconds: 60,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, updated.Transports.Bark.DeviceKeysConfigured)
	require.Empty(t, updated.Transports.Bark.DeviceKeys)
	require.True(t, updated.Transports.Telegram.BotTokenConfigured)
	require.Empty(t, updated.Transports.Telegram.BotToken)
	require.Equal(t, "timeSensitive", updated.Transports.Bark.Level)

	raw, err := repo.GetValue(ctx, SettingKeyNotificationConfig)
	require.NoError(t, err)
	require.Contains(t, raw, "enc:cipher:device-1")
	require.Contains(t, raw, "enc:cipher:token-1")

	updated, err = svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:   true,
				ServerURL: "https://bark.example",
				Level:     "passive",
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled: true,
				ChatIDs: []string{"10001"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, updated.Transports.Bark.DeviceKeysConfigured)
	require.True(t, updated.Transports.Telegram.BotTokenConfigured)
	require.Equal(t, "passive", updated.Transports.Bark.Level)
}

func TestNotificationConfigCanClearSavedSecrets(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationService(repo, nil, nil, notificationPlainEncryptor{})

	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: false,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  "https://bark.example",
				DeviceKeys: []string{"device-1"},
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled:  true,
				BotToken: "token-1",
				ChatIDs:  []string{"10001"},
			},
		},
	})
	require.NoError(t, err)

	updated, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: false,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:         true,
				ServerURL:       "https://bark.example",
				ClearDeviceKeys: true,
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled:       true,
				ClearBotToken: true,
				ChatIDs:       []string{"10001"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, updated.Transports.Bark.DeviceKeysConfigured)
	require.False(t, updated.Transports.Telegram.BotTokenConfigured)

	raw, err := repo.GetValue(ctx, SettingKeyNotificationConfig)
	require.NoError(t, err)
	require.NotContains(t, raw, "device-1")
	require.NotContains(t, raw, "token-1")
}

func TestNotificationConfigDefaultsDisabled(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

	cfg, err := svc.GetConfig(ctx)

	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.False(t, cfg.QuietHours.Enabled)
	require.Equal(t, "22:00", cfg.QuietHours.StartTime)
	require.Equal(t, "08:00", cfg.QuietHours.EndTime)
	require.NotEmpty(t, cfg.QuietHours.Timezone)
	require.Equal(t, "active", cfg.Transports.Bark.Level)
	require.Contains(t, cfg.Transports.Bark.TitleTemplate, "Sub2API 渠道监控")
	require.Contains(t, cfg.Transports.Bark.BodyTemplate, "监控：{monitor_name}")
}

func TestNotificationConfigRejectsInvalidQuietHours(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		quiet     NotificationQuietHoursConfig
		wantError string
	}{
		{
			name: "bad-start-time",
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "8:00",
				EndTime:   "08:00",
				Timezone:  "UTC",
			},
			wantError: "invalid quiet hours start_time",
		},
		{
			name: "bad-end-time",
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "22:00",
				EndTime:   "24:00",
				Timezone:  "UTC",
			},
			wantError: "invalid quiet hours end_time",
		},
		{
			name: "bad-timezone",
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "22:00",
				EndTime:   "08:00",
				Timezone:  "Not/AZone",
			},
			wantError: "invalid quiet hours timezone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

			_, err := svc.UpdateConfig(ctx, &NotificationConfig{
				Enabled:    false,
				QuietHours: tc.quiet,
			})

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestNotificationConfigRejectsEnabledWithoutRecipient(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Email: NotificationEmailTransportConfig{
				Enabled:    true,
				Recipients: []string{},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportEmail},
				MinIntervalSeconds: 0,
			},
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "recipient is required")
}

func TestNotificationConfigRejectsEnabledTransportsWithoutRecipients(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		config    NotificationConfig
		wantError string
	}{
		{
			name: "email",
			config: NotificationConfig{
				Enabled: true,
				Transports: NotificationTransportConfigs{
					Email: NotificationEmailTransportConfig{Enabled: true},
				},
				Routes: map[string]NotificationRoute{
					NotificationEventChannelMonitorFailed: {
						Enabled:    true,
						Transports: []string{NotificationTransportEmail},
					},
				},
			},
			wantError: "email notification recipient is required",
		},
		{
			name: "bark",
			config: NotificationConfig{
				Enabled: true,
				Transports: NotificationTransportConfigs{
					Email: NotificationEmailTransportConfig{
						Enabled:    true,
						Recipients: []string{"admin@example.com"},
					},
					Bark: NotificationBarkTransportConfig{Enabled: true},
				},
				Routes: map[string]NotificationRoute{
					NotificationEventChannelMonitorFailed: {
						Enabled:    true,
						Transports: []string{NotificationTransportEmail},
					},
				},
			},
			wantError: "bark device key is required",
		},
		{
			name: "telegram-token",
			config: NotificationConfig{
				Enabled: true,
				Transports: NotificationTransportConfigs{
					Email: NotificationEmailTransportConfig{
						Enabled:    true,
						Recipients: []string{"admin@example.com"},
					},
					Telegram: NotificationTelegramTransportConfig{
						Enabled: true,
						ChatIDs: []string{"10001"},
					},
				},
				Routes: map[string]NotificationRoute{
					NotificationEventChannelMonitorFailed: {
						Enabled:    true,
						Transports: []string{NotificationTransportEmail},
					},
				},
			},
			wantError: "telegram bot token is required",
		},
		{
			name: "telegram-chat",
			config: NotificationConfig{
				Enabled: true,
				Transports: NotificationTransportConfigs{
					Email: NotificationEmailTransportConfig{
						Enabled:    true,
						Recipients: []string{"admin@example.com"},
					},
					Telegram: NotificationTelegramTransportConfig{
						Enabled:  true,
						BotToken: "token-1",
					},
				},
				Routes: map[string]NotificationRoute{
					NotificationEventChannelMonitorFailed: {
						Enabled:    true,
						Transports: []string{NotificationTransportEmail},
					},
				},
			},
			wantError: "telegram chat id is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

			_, err := svc.UpdateConfig(ctx, &tc.config)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestNotificationConfigRejectsEnabledRouteWithoutDeliverableTransport(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Email: NotificationEmailTransportConfig{
				Enabled:    true,
				Recipients: []string{"admin@example.com"},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:    true,
				Transports: []string{NotificationTransportEmail},
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled:    true,
				Transports: []string{NotificationTransportTelegram},
			},
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), NotificationEventChannelMonitorRecovered)
	require.Contains(t, err.Error(), "requires at least one deliverable transport")
}

func TestNotificationDispatchSendsBarkAndTelegram(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var barkPath string
	var barkPayload map[string]string
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		barkPath = r.URL.EscapedPath()
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&barkPayload))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	var telegramPath string
	var telegramPayload map[string]string
	svc := NewNotificationService(repo, nil, nil, nil)
	svc.httpClient = &http.Client{
		Transport: notificationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api.telegram.org" {
				return http.DefaultTransport.RoundTrip(req)
			}
			telegramPath = req.URL.EscapedPath()
			require.NoError(t, json.NewDecoder(req.Body).Decode(&telegramPayload))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:       true,
				ServerURL:     barkServer.URL,
				DeviceKeys:    []string{"device/key"},
				Level:         "critical",
				TitleTemplate: "中文提醒：{monitor_name} {new_status}",
				BodyTemplate:  "模型 {model} 从 {old_status} 变为 {new_status}\n原因：{message}",
			},
			Telegram: NotificationTelegramTransportConfig{
				Enabled:  true,
				BotToken: "bot-token",
				ChatIDs:  []string{"chat-1"},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark, NotificationTransportTelegram},
				MinIntervalSeconds: 0,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, NotificationPayload{
		Title:      "Monitor failed",
		Content:    "model failed",
		Event:      "failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
		Variables: map[string]string{
			"monitor_name": "主监控",
			"model":        "gpt-5.4",
			"old_status":   "operational",
			"new_status":   "failed",
			"message":      "上游超时",
			"monitor_url":  "https://example.com/monitor",
		},
	})

	require.Equal(t, "/device%2Fkey", barkPath)
	require.Equal(t, "中文提醒：主监控 failed", barkPayload["title"])
	require.Equal(t, "模型 gpt-5.4 从 operational 变为 failed\n原因：上游超时", barkPayload["body"])
	require.Equal(t, "critical", barkPayload["level"])
	require.Equal(t, "https://example.com/monitor", barkPayload["url"])
	require.Equal(t, "/botbot-token/sendMessage", telegramPath)
	require.Equal(t, "chat-1", telegramPayload["chat_id"])
	require.Contains(t, telegramPayload["text"], "Monitor failed")
	require.Contains(t, telegramPayload["text"], "model failed")
}

func TestNotificationDispatchRespectsQuietHours(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		now       time.Time
		quiet     NotificationQuietHoursConfig
		wantCount int
	}{
		{
			name: "cross-midnight-suppresses-at-night",
			now:  time.Date(2026, 6, 7, 15, 0, 0, 0, time.UTC), // 23:00 Asia/Shanghai
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "22:00",
				EndTime:   "08:00",
				Timezone:  "Asia/Shanghai",
			},
			wantCount: 0,
		},
		{
			name: "cross-midnight-allows-after-window",
			now:  time.Date(2026, 6, 7, 1, 0, 0, 0, time.UTC), // 09:00 Asia/Shanghai
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "22:00",
				EndTime:   "08:00",
				Timezone:  "Asia/Shanghai",
			},
			wantCount: 1,
		},
		{
			name: "same-day-suppresses-inside-window",
			now:  time.Date(2026, 6, 7, 5, 0, 0, 0, time.UTC), // 13:00 Asia/Shanghai
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "12:00",
				EndTime:   "14:00",
				Timezone:  "Asia/Shanghai",
			},
			wantCount: 0,
		},
		{
			name: "same-start-end-suppresses-all-day",
			now:  time.Date(2026, 6, 7, 1, 0, 0, 0, time.UTC),
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "08:00",
				EndTime:   "08:00",
				Timezone:  "UTC",
			},
			wantCount: 0,
		},
		{
			name: "timezone-controls-window",
			now:  time.Date(2026, 6, 7, 15, 30, 0, 0, time.UTC), // 23:30 Shanghai, 11:30 New York
			quiet: NotificationQuietHoursConfig{
				Enabled:   true,
				StartTime: "22:00",
				EndTime:   "08:00",
				Timezone:  "America/New_York",
			},
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newNotificationEmailMemorySettingRepo()
			var count int
			barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count++
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(barkServer.Close)

			svc := NewNotificationService(repo, nil, nil, nil)
			svc.now = func() time.Time { return tc.now }
			_, err := svc.UpdateConfig(ctx, &NotificationConfig{
				Enabled: true,
				Transports: NotificationTransportConfigs{
					Bark: NotificationBarkTransportConfig{
						Enabled:    true,
						ServerURL:  barkServer.URL,
						DeviceKeys: []string{"device"},
						Level:      "active",
					},
				},
				Routes: map[string]NotificationRoute{
					NotificationEventChannelMonitorFailed: {
						Enabled:            true,
						Transports:         []string{NotificationTransportBark},
						MinIntervalSeconds: 0,
					},
					NotificationEventChannelMonitorRecovered: {
						Enabled: false,
					},
				},
				QuietHours: tc.quiet,
			})
			require.NoError(t, err)

			svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, NotificationPayload{
				Title:      "title",
				Content:    "content",
				Event:      "failed",
				SourceType: "channel_monitor",
				SourceID:   "1:gpt",
			})

			require.Equal(t, tc.wantCount, count)
		})
	}
}

func TestNotificationTestBypassesQuietHours(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var count int
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	svc := NewNotificationService(repo, nil, nil, nil)
	svc.now = func() time.Time {
		return time.Date(2026, 6, 7, 15, 0, 0, 0, time.UTC) // 23:00 Asia/Shanghai
	}
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled: false,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 0,
			},
		},
		QuietHours: NotificationQuietHoursConfig{
			Enabled:   true,
			StartTime: "22:00",
			EndTime:   "08:00",
			Timezone:  "Asia/Shanghai",
		},
	})
	require.NoError(t, err)

	err = svc.Test(ctx, NotificationTransportBark)

	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestNotificationDispatchAsyncReturnsBeforeTransportCompletes(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseResponse
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(releaseResponse)
		barkServer.Close()
	})

	svc := NewNotificationService(repo, nil, nil, nil)
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 0,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	start := time.Now()
	svc.DispatchAsync(ctx, NotificationEventChannelMonitorFailed, NotificationPayload{
		Title:      "title",
		Content:    "content",
		Event:      "failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
	})

	require.Less(t, time.Since(start), 100*time.Millisecond)
	require.Eventually(t, func() bool {
		select {
		case <-requestStarted:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}

func TestNotificationRateLimitKeySuppressesRepeatedDispatch(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var count int
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	svc := NewNotificationService(repo, nil, nil, nil)
	now := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 300,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	payload := NotificationPayload{
		Title:      "title",
		Content:    "content",
		Event:      "failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
	}
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 1, count)

	now = now.Add(301 * time.Second)
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 2, count)
}

func TestNotificationRateLimitIgnoresPayloadSubEvent(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var count int
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	svc := NewNotificationService(repo, nil, nil, nil)
	now := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 300,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	payload := NotificationPayload{
		Title:      "title",
		Content:    "content",
		Event:      "channel_monitor.failed:failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
	}
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	payload.Event = "channel_monitor.failed:error"
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 1, count)
}

func TestNotificationDispatchDoesNotRateLimitFailedDelivery(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var count int
	failDelivery := true
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		if failDelivery {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	svc := NewNotificationService(repo, nil, nil, nil)
	now := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 300,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	payload := NotificationPayload{
		Title:      "title",
		Content:    "content",
		Event:      "failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
	}
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 2, count, "failed deliveries must not mark the event as rate limited")

	failDelivery = false
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 3, count, "successful delivery should start the rate-limit window")
}

func TestNotificationDispatchRateLimitsPartialDelivery(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	var count int
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if strings.Contains(r.URL.EscapedPath(), "bad") {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkServer.Close)

	svc := NewNotificationService(repo, nil, nil, nil)
	now := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Bark: NotificationBarkTransportConfig{
				Enabled:    true,
				ServerURL:  barkServer.URL,
				DeviceKeys: []string{"good-device", "bad-device"},
				Level:      "active",
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled:            true,
				Transports:         []string{NotificationTransportBark},
				MinIntervalSeconds: 300,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled: false,
			},
		},
	})
	require.NoError(t, err)

	payload := NotificationPayload{
		Title:      "title",
		Content:    "content",
		Event:      "failed",
		SourceType: "channel_monitor",
		SourceID:   "1:gpt",
	}
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	svc.Dispatch(ctx, NotificationEventChannelMonitorFailed, payload)
	require.Equal(t, 2, count, "partial success should still start the rate-limit window")
}

func TestNotificationTelegramErrorsDoNotExposeBotToken(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationService(repo, nil, nil, nil)
	svc.httpClient = &http.Client{
		Transport: notificationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("post %s failed", req.URL.String())
		}),
	}
	_, err := svc.UpdateConfig(ctx, &NotificationConfig{
		Enabled: true,
		Transports: NotificationTransportConfigs{
			Telegram: NotificationTelegramTransportConfig{
				Enabled:  true,
				BotToken: "secret-token",
				ChatIDs:  []string{"chat-1"},
			},
		},
		Routes: map[string]NotificationRoute{
			NotificationEventChannelMonitorFailed: {
				Enabled: false,
			},
			NotificationEventChannelMonitorRecovered: {
				Enabled:            true,
				Transports:         []string{NotificationTransportTelegram},
				MinIntervalSeconds: 0,
			},
		},
	})
	require.NoError(t, err)

	err = svc.Test(ctx, NotificationTransportTelegram)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-token")
	require.Contains(t, err.Error(), "[redacted telegram bot token]")
}

func TestNotificationTestRejectsDisabledTransport(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationService(newNotificationEmailMemorySettingRepo(), nil, nil, nil)

	err := svc.Test(ctx, NotificationTransportBark)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled")
}
