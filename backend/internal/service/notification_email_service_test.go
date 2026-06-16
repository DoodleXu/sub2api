package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestNotificationEmailPreviewEscapesHTMLAndSanitizesSubject(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:   NotificationEmailEventBalanceLow,
		Locale:  "en-US,en;q=0.9",
		Subject: "Low balance for {{recipient_name}}\r\nInjected",
		HTML:    `<p>{{recipient_name}}</p><a href="{{recharge_url}}">Recharge</a>`,
		Variables: map[string]string{
			"recipient_name": `<script>alert("x")</script>`,
			"recharge_url":   `javascript:alert(1)`,
		},
	})
	require.NoError(t, err)
	require.NotContains(t, preview.Subject, "\r")
	require.NotContains(t, preview.Subject, "\n")
	require.Contains(t, preview.Subject, `Low balance for <script>alert("x")</script>Injected`)
	require.Contains(t, preview.HTML, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	require.NotContains(t, preview.HTML, `javascript:alert(1)`)
	require.Contains(t, preview.HTML, `href=""`)
}

func TestNotificationEmailTemplateOverrideAndRestore(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)

	official, err := svc.GetTemplate(ctx, NotificationEmailEventBalanceRechargeSuccess, "en")
	require.NoError(t, err)
	require.False(t, official.IsCustom)

	updated, err := svc.UpdateTemplate(
		ctx,
		NotificationEmailEventBalanceRechargeSuccess,
		"zh-Hans",
		"充值完成：{{recharge_amount}}",
		"<p>{{recipient_name}} 已充值 {{recharge_amount}}</p>",
	)
	require.NoError(t, err)
	require.True(t, updated.IsCustom)
	require.Equal(t, "zh", updated.Locale)
	require.Equal(t, "充值完成：{{recharge_amount}}", updated.Subject)
	require.NotNil(t, updated.UpdatedAt)

	restored, err := svc.RestoreOfficialTemplate(ctx, NotificationEmailEventBalanceRechargeSuccess, "zh")
	require.NoError(t, err)
	require.False(t, restored.IsCustom)
	require.NotEqual(t, updated.Subject, restored.Subject)
	_, err = repo.GetValue(ctx, notificationEmailTemplateKey(NotificationEmailEventBalanceRechargeSuccess, "zh"))
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestNotificationEmailTemplateRejectsUnsupportedPlaceholder(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	_, err := svc.UpdateTemplate(
		ctx,
		NotificationEmailEventSubscriptionPurchaseSuccess,
		"en",
		"Purchased {{not_allowed}}",
		"<p>{{subscription_group}}</p>",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported placeholder")
}

func TestNotificationEmailAuthTemplatesAreListedAndPreviewable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	infos := svc.ListEventInfos()
	events := make(map[string]NotificationEmailEventInfo, len(infos))
	for _, info := range infos {
		events[info.Event] = info
	}
	require.Contains(t, events, NotificationEmailEventAuthVerifyCode)
	require.Contains(t, events, NotificationEmailEventAuthPasswordReset)
	require.False(t, events[NotificationEmailEventAuthVerifyCode].Optional)
	require.False(t, events[NotificationEmailEventAuthPasswordReset].Optional)
	require.Contains(t, events[NotificationEmailEventAuthVerifyCode].Placeholders, "verification_code")
	require.Contains(t, events[NotificationEmailEventAuthPasswordReset].Placeholders, "reset_url")

	verifyPreview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:  NotificationEmailEventAuthVerifyCode,
		Locale: "zh-CN",
		Variables: map[string]string{
			"verification_code":  "654321",
			"expires_in_minutes": "15",
		},
	})
	require.NoError(t, err)
	require.Contains(t, verifyPreview.Subject, "邮箱验证码")
	require.Contains(t, verifyPreview.HTML, "654321")

	resetPreview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:  NotificationEmailEventAuthPasswordReset,
		Locale: "en",
		Variables: map[string]string{
			"reset_url":          "https://example.com/reset?token=abc",
			"expires_in_minutes": "30",
		},
	})
	require.NoError(t, err)
	require.Contains(t, resetPreview.Subject, "Password reset")
	require.Contains(t, resetPreview.HTML, "https://example.com/reset?token=abc")
}

func TestNotificationEmailAdditionalEventsAreListedAndPreviewable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	infos := svc.ListEventInfos()
	events := make(map[string]NotificationEmailEventInfo, len(infos))
	for _, info := range infos {
		events[info.Event] = info
	}

	checks := []struct {
		event       string
		placeholder string
	}{
		{NotificationEmailEventNotificationEmailVerifyCode, "verification_code"},
		{NotificationEmailEventAccountQuotaAlert, "account_name"},
		{NotificationEmailEventContentModerationViolation, "moderation_category"},
		{NotificationEmailEventContentModerationDisabled, "violation_count"},
		{NotificationEmailEventOpsAlert, "rule_name"},
		{NotificationEmailEventOpsScheduledReport, "report_html"},
		{NotificationEmailEventAdminBroadcast, "message_html"},
	}

	for _, check := range checks {
		info, ok := events[check.event]
		require.Truef(t, ok, "expected %s to be listed", check.event)
		if check.event == NotificationEmailEventAdminBroadcast {
			require.True(t, info.Optional)
		} else {
			require.False(t, info.Optional)
		}
		require.Contains(t, info.Placeholders, check.placeholder)

		preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{Event: check.event, Locale: "zh"})
		require.NoError(t, err)
		require.NotEmpty(t, preview.Subject)
		require.NotEmpty(t, preview.HTML)
	}
}

func TestNotificationEmailPreviewUsesConfiguredURLsAndTrustedBroadcastHTML(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.Set(ctx, SettingKeyFrontendURL, "https://app.example.test"))
	require.NoError(t, repo.Set(ctx, SettingKeyAPIBaseURL, "https://api.example.test"))
	svc := NewNotificationEmailService(repo, nil)

	preview, err := svc.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event:  NotificationEmailEventAdminBroadcast,
		Locale: "zh",
	})
	require.NoError(t, err)
	require.Contains(t, preview.HTML, `<p>这是一封由管理员发送给用户的重要邮件通知。</p>`)
	require.NotContains(t, preview.HTML, `&lt;p&gt;这是一封`)
	require.Contains(t, preview.HTML, `https://app.example.test/email-unsubscribe?token=preview`)
	require.Contains(t, preview.HTML, `https://app.example.test/notice`)
	require.NotContains(t, preview.HTML, `https://example.com/unsubscribe`)
	require.NotContains(t, preview.HTML, `https://example.com/notice`)
}

func TestNotificationEmailRawHTMLVariablesAreTrustedOnlyForHTMLPlaceholders(t *testing.T) {
	require.True(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsScheduledReport, "report_html"))
	require.True(t, notificationEmailRawHTMLAllowed(NotificationEmailEventAdminBroadcast, "message_html"))
	require.True(t, notificationEmailRawHTMLAllowed(NotificationEmailEventAdminBroadcast, "action_html"))
	require.False(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsScheduledReport, "recipient_name"))
	require.False(t, notificationEmailRawHTMLAllowed(NotificationEmailEventAdminBroadcast, "recipient_name"))
	require.False(t, notificationEmailRawHTMLAllowed(NotificationEmailEventOpsAlert, "report_html"))

	preview, err := renderNotificationEmail(
		NotificationEmailEventOpsScheduledReport,
		"Report for {{recipient_name}}",
		`<section>{{report_html}}</section><p>{{recipient_name}}</p>`,
		map[string]string{
			"recipient_name": `<script>alert("x")</script>`,
			"report_html":    `<p>escaped report</p>`,
		},
		map[string]string{
			"report_html": `<table><tr><td>trusted report</td></tr></table>`,
		},
	)
	require.NoError(t, err)
	require.Contains(t, preview.HTML, `<table><tr><td>trusted report</td></tr></table>`)
	require.NotContains(t, preview.HTML, `escaped report`)
	require.Contains(t, preview.HTML, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	require.Contains(t, preview.Subject, `<script>alert("x")</script>`)

	preview, err = renderNotificationEmail(
		NotificationEmailEventOpsScheduledReport,
		"Recipient {{recipient_name}}",
		`<p>{{recipient_name}}</p>`,
		map[string]string{"recipient_name": `<em>escaped</em>`},
		map[string]string{"recipient_name": `<strong>raw</strong>`},
	)
	require.NoError(t, err)
	require.Contains(t, preview.HTML, `&lt;em&gt;escaped&lt;/em&gt;`)
	require.NotContains(t, preview.HTML, `<strong>raw</strong>`)
}

func TestNotificationEmailBroadcastResolvesRecipientsByScopeAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	svc.SetUserRepository(&notificationEmailBroadcastUserRepoStub{
		users: []User{
			{ID: 1, Email: "User@One.test", Username: "One", Role: RoleUser, Status: StatusActive},
			{ID: 2, Email: "two@example.test", Username: "Two", Role: RoleUser, Status: StatusDisabled},
			{ID: 3, Email: "admin@example.test", Username: "Admin", Role: RoleAdmin, Status: StatusActive},
		},
	})

	recipients, err := svc.resolveBroadcastRecipients(ctx, NotificationEmailBroadcastInput{Scope: "active_users"})
	require.NoError(t, err)
	require.Equal(t, []string{"User@One.test"}, notificationEmailBroadcastRecipientEmails(recipients))

	recipients, err = svc.resolveBroadcastRecipients(ctx, NotificationEmailBroadcastInput{
		Scope:   "custom",
		UserIDs: []int64{1, 3},
		Emails:  []string{"user@one.test", "extra@example.test", "EXTRA@example.test"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"User@One.test", "admin@example.test", "extra@example.test"}, notificationEmailBroadcastRecipientEmails(recipients))
}

func TestNotificationEmailBroadcastPreflightsEmailConfig(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, NewEmailService(repo, nil))

	_, err := svc.StartBroadcast(ctx, NotificationEmailBroadcastInput{
		Scope:        "custom",
		MessageTitle: "Notice",
		MessageHTML:  "<p>Hello</p>",
		Emails:       []string{"user@example.test"},
		RPM:          6,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "email service is not configured")
}

func TestNotificationEmailBroadcastActionHTMLIsOptionalAndEscaped(t *testing.T) {
	require.Empty(t, notificationEmailBroadcastActionHTML("", "https://example.com/notice"))
	require.Empty(t, notificationEmailBroadcastActionHTML("Open", "javascript:alert(1)"))

	html := notificationEmailBroadcastActionHTML(`View <details>`, `https://example.com/notice?a=1&b=2`)
	require.Contains(t, html, `class="button"`)
	require.Contains(t, html, `href="https://example.com/notice?a=1&amp;b=2"`)
	require.Contains(t, html, `View &lt;details&gt;`)
	require.NotContains(t, html, `View <details>`)
}

func TestNotificationEmailBroadcastStatusIsPersistedAndReadable(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	err := svc.saveBroadcastStatus(ctx, NotificationEmailBroadcastStatus{
		BatchID:     "broadcast_test",
		Status:      "running",
		Scope:       "custom",
		Locale:      "zh",
		TargetCount: 2,
		RPM:         6,
	})
	require.NoError(t, err)

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_test")
	require.NoError(t, err)
	require.Equal(t, "broadcast_test", status.BatchID)
	require.Equal(t, "running", status.Status)
	require.Equal(t, 2, status.TargetCount)
	require.NotEmpty(t, status.StartedAt)
	require.NotEmpty(t, status.UpdatedAt)
}

func TestNotificationEmailBroadcastRunUpdatesFailureStatus(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	startedAt := time.Date(2026, 6, 15, 1, 2, 3, 0, time.UTC)
	svc.runBroadcast(ctx, "broadcast_failures", NotificationEmailBroadcastInput{
		Scope:        "custom",
		Locale:       "zh",
		MessageTitle: "Notice",
		MessageHTML:  "<p>Hello</p>",
		RPM:          60000,
	}, []notificationEmailBroadcastRecipient{
		{Email: "one@example.test", Name: "One"},
		{Email: "two@example.test", Name: "Two"},
	}, startedAt)

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_failures")
	require.NoError(t, err)
	require.Equal(t, "completed", status.Status)
	require.Equal(t, 0, status.SentCount)
	require.Equal(t, 2, status.FailureCount)
	require.Equal(t, 2, status.TargetCount)
	require.Equal(t, startedAt.Format(time.RFC3339), status.StartedAt)
	require.NotEmpty(t, status.CompletedAt)
	require.Contains(t, status.LastError, "email service is not configured")
}

func TestNotificationEmailBroadcastTracksUnsubscribedRecipientsAsSkipped(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.Set(ctx, notificationEmailPreferenceKey(NotificationEmailEventAdminBroadcast, "skip@example.test"), "unsubscribed"))
	svc := NewNotificationEmailService(repo, nil)

	startedAt := time.Date(2026, 6, 15, 4, 5, 6, 0, time.UTC)
	svc.runBroadcast(ctx, "broadcast_skipped", NotificationEmailBroadcastInput{
		Scope:        "custom",
		Locale:       "zh",
		MessageTitle: "Notice",
		MessageHTML:  "<p>Hello</p>",
		RPM:          60000,
	}, []notificationEmailBroadcastRecipient{
		{Email: "skip@example.test", Name: "Skip"},
	}, startedAt)

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_skipped")
	require.NoError(t, err)
	require.Equal(t, "completed", status.Status)
	require.Equal(t, 0, status.SentCount)
	require.Equal(t, 1, status.SkippedCount)
	require.Equal(t, 1, status.UnsubscribedCount)
	require.Equal(t, 0, status.FailureCount)
	require.Equal(t, startedAt.Format(time.RFC3339), status.StartedAt)
	require.Empty(t, status.LastError)
}

func TestNotificationEmailBroadcastLockRejectsConcurrentRunningBatch(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)

	locked, err := svc.acquireBroadcastLock(ctx, "broadcast_first")
	require.NoError(t, err)
	require.True(t, locked)

	locked, err = svc.acquireBroadcastLock(ctx, "broadcast_second")
	require.NoError(t, err)
	require.False(t, locked)

	svc.releaseBroadcastLockBestEffort(ctx, "broadcast_second")
	active, err := repo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	require.NoError(t, err)
	require.Equal(t, "broadcast_first", active)

	svc.releaseBroadcastLockBestEffort(ctx, "broadcast_first")
	_, err = repo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestNotificationEmailBroadcastStaleRunningStatusIsInterrupted(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:     "broadcast_stale",
		Status:      "running",
		Scope:       "custom",
		Locale:      "zh",
		TargetCount: 5,
		RPM:         6,
		StartedAt:   startedAt,
		UpdatedAt:   now.Add(-20 * time.Minute).Format(time.RFC3339),
	})
	require.NoError(t, repo.Set(ctx, notificationEmailBroadcastActiveKey, "broadcast_stale"))

	require.NoError(t, svc.releaseInterruptedActiveBroadcast(ctx, now))

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_stale")
	require.NoError(t, err)
	require.Equal(t, "interrupted", status.Status)
	require.Equal(t, startedAt, status.StartedAt)
	require.Equal(t, now.Format(time.RFC3339), status.CompletedAt)
	require.Contains(t, status.LastError, "interrupted")
	_, err = repo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	require.ErrorIs(t, err, ErrSettingNotFound)
}

func TestNotificationEmailBroadcastFreshRunningStatusKeepsLock(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:     "broadcast_fresh",
		Status:      "running",
		Scope:       "custom",
		Locale:      "zh",
		TargetCount: 5,
		RPM:         6,
		StartedAt:   now.Add(-5 * time.Minute).Format(time.RFC3339),
		UpdatedAt:   now.Add(-1 * time.Minute).Format(time.RFC3339),
	})
	require.NoError(t, repo.Set(ctx, notificationEmailBroadcastActiveKey, "broadcast_fresh"))

	require.NoError(t, svc.releaseInterruptedActiveBroadcast(ctx, now))

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_fresh")
	require.NoError(t, err)
	require.Equal(t, "running", status.Status)
	active, err := repo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	require.NoError(t, err)
	require.Equal(t, "broadcast_fresh", active)
}

func TestNotificationEmailBroadcastFreshCancelingStatusKeepsLock(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:     "broadcast_canceling",
		Status:      "canceling",
		Scope:       "custom",
		Locale:      "zh",
		TargetCount: 5,
		RPM:         6,
		StartedAt:   now.Add(-5 * time.Minute).Format(time.RFC3339),
		UpdatedAt:   now.Add(-1 * time.Minute).Format(time.RFC3339),
	})
	require.NoError(t, repo.Set(ctx, notificationEmailBroadcastActiveKey, "broadcast_canceling"))

	require.NoError(t, svc.releaseInterruptedActiveBroadcast(ctx, now))

	status, err := svc.GetBroadcastStatus(ctx, "broadcast_canceling")
	require.NoError(t, err)
	require.Equal(t, "canceling", status.Status)
	active, err := repo.GetValue(ctx, notificationEmailBroadcastActiveKey)
	require.NoError(t, err)
	require.Equal(t, "broadcast_canceling", active)
}

func TestNotificationEmailBroadcastListUsesIndexAndStoredStatuses(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:      "broadcast_old",
		Status:       "completed",
		MessageTitle: "Old",
		StartedAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
		UpdatedAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
	})
	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:      "broadcast_new",
		Status:       "completed",
		MessageTitle: "New",
		StartedAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
		UpdatedAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
	})
	require.NoError(t, svc.addBroadcastToIndex(ctx, "broadcast_old"))
	require.NoError(t, svc.addBroadcastToIndex(ctx, "broadcast_new"))

	list, err := svc.ListBroadcasts(ctx)
	require.NoError(t, err)
	require.Len(t, list.Jobs, 2)
	require.Equal(t, "broadcast_new", list.Jobs[0].BatchID)
	require.Equal(t, "New", list.Jobs[0].MessageTitle)
	require.Equal(t, "broadcast_old", list.Jobs[1].BatchID)
}

func TestNotificationEmailBroadcastCancelMarksRunningJobAsCanceling(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:   "broadcast_cancel",
		Status:    "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	status, err := svc.CancelBroadcast(ctx, "broadcast_cancel")
	require.NoError(t, err)
	require.Equal(t, "canceling", status.Status)
	_, err = repo.GetValue(ctx, notificationEmailBroadcastCancelKey("broadcast_cancel"))
	require.NoError(t, err)
}

func TestNotificationEmailBroadcastResumeTargetsRemainingOrFailedRecipients(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	batchID := "broadcast_resume"
	require.NoError(t, svc.saveBroadcastPayload(ctx, batchID, NotificationEmailBroadcastInput{
		Scope:        "custom",
		Locale:       "zh",
		MessageTitle: "Notice",
		MessageHTML:  "<p>Hello</p>",
		RPM:          60000,
	}))
	require.NoError(t, svc.saveBroadcastRecipients(ctx, batchID, []notificationEmailBroadcastRecipientState{
		{Email: "sent@example.test", Status: "sent"},
		{Email: "failed@example.test", Status: "failed", Error: "smtp"},
		{Email: "pending@example.test", Status: "pending"},
		{Email: "skip@example.test", Status: "skipped", Error: "unsubscribed"},
	}))
	setNotificationEmailBroadcastStatusForTest(t, repo, NotificationEmailBroadcastStatus{
		BatchID:     batchID,
		Status:      "interrupted",
		Scope:       "custom",
		Locale:      "zh",
		TargetCount: 4,
		RPM:         60000,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	})

	result, err := svc.ResumeBroadcast(ctx, batchID, NotificationEmailBroadcastResumeInput{Mode: "failed"})
	require.NoError(t, err)
	require.Equal(t, batchID, result.BatchID)
	require.Equal(t, 1, result.TargetCount)
	for {
		status, err := svc.GetBroadcastStatus(ctx, batchID)
		require.NoError(t, err)
		if status.Status == "completed" {
			require.Equal(t, 1, status.SentCount)
			require.Equal(t, 1, status.FailureCount)
			require.Equal(t, 1, status.SkippedCount)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNotificationEmailFallbackClassification(t *testing.T) {
	templateErr := notificationEmailTemplateErr(errors.New("bad template"))
	configErr := notificationEmailConfigErr(errors.New("missing email service"))
	deliveryErr := notificationEmailDeliveryErr(errors.New("smtp timeout"))

	require.True(t, shouldFallbackNotificationEmail(templateErr))
	require.True(t, shouldFallbackNotificationEmail(configErr))
	require.False(t, shouldFallbackNotificationEmail(deliveryErr))
	require.True(t, isNotificationEmailDeliveryError(deliveryErr))
	require.False(t, isNotificationEmailDeliveryError(templateErr))
	require.False(t, shouldFallbackNotificationEmail(nil))
}

func TestEmailQueueTasksPreserveLocaleHints(t *testing.T) {
	queue := &EmailQueueService{taskChan: make(chan EmailTask, 2)}
	require.NoError(t, queue.EnqueueVerifyCode("user@example.com", "Sub2API", "zh-CN"))
	require.NoError(t, queue.EnqueuePasswordReset("user@example.com", "Sub2API", "https://example.com/reset", "en-US"))

	verifyTask := <-queue.taskChan
	require.Equal(t, TaskTypeVerifyCode, verifyTask.TaskType)
	require.Equal(t, "zh-CN", verifyTask.Locale)

	resetTask := <-queue.taskChan
	require.Equal(t, TaskTypePasswordReset, resetTask.TaskType)
	require.Equal(t, "en-US", resetTask.Locale)
}

func TestOpsScheduledReportDeliverySourceIDIncludesReportIdentity(t *testing.T) {
	report := &opsScheduledReport{Name: "日报", ReportType: "daily_summary", Schedule: "0 9 * * *"}
	sourceID := opsScheduledReportDeliverySourceID(report)
	require.Contains(t, sourceID, "daily_summary")
	require.Contains(t, sourceID, "日报")
	require.Contains(t, sourceID, "0 9 * * *")
	require.NotEqual(t, sourceID, opsScheduledReportDeliverySourceID(&opsScheduledReport{Name: "周报", ReportType: "weekly_summary", Schedule: "0 9 * * 1"}))
	require.Equal(t, "scheduled_report", opsScheduledReportDeliverySourceID(nil))
}

func TestNotificationEmailUnsubscribeOnlyAllowsOptionalEvents(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	token, err := svc.createUnsubscribeToken(ctx, "User@Example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	result, err := svc.Unsubscribe(ctx, token)
	require.NoError(t, err)
	require.True(t, result.Done)
	require.Equal(t, NotificationEmailEventBalanceLow, result.Event)
	require.Equal(t, "Low balance alert", result.EventLabel)
	unsubscribed, err := svc.IsUnsubscribed(ctx, "user@example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)
	require.True(t, unsubscribed)

	transactionalToken, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventBalanceRechargeSuccess)
	require.NoError(t, err)
	_, err = svc.Unsubscribe(ctx, transactionalToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transactional")

	authToken, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventAuthVerifyCode)
	require.NoError(t, err)
	_, err = svc.Unsubscribe(ctx, authToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transactional")
}

func TestNotificationEmailUnsubscribeTokenExpires(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	token, err := svc.createUnsubscribeToken(ctx, "user@example.com", NotificationEmailEventBalanceLow)
	require.NoError(t, err)

	svc.now = func() time.Time { return now.Add(notificationEmailUnsubscribeTTL + time.Second) }
	_, err = svc.Unsubscribe(ctx, token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
}

func TestNotificationEmailUnsubscribeURLsPreferFrontendForBodyAndAPIForHeaders(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.Set(ctx, SettingKeyFrontendURL, "https://app.example.test"))
	require.NoError(t, repo.Set(ctx, SettingKeyAPIBaseURL, "https://api.example.test"))
	svc := NewNotificationEmailService(repo, nil)

	variables, headers, err := svc.runtimeVariables(ctx, NotificationEmailEventBalanceLow, "en", NotificationEmailSendInput{
		RecipientEmail: "user@example.com",
	})

	require.NoError(t, err)
	require.Contains(t, variables["unsubscribe_url"], "https://app.example.test/email-unsubscribe?token=")
	require.Contains(t, headers["List-Unsubscribe"], "<https://api.example.test/api/v1/settings/email-unsubscribe?token=")
	require.Contains(t, headers["List-Unsubscribe"], ">")
	require.Equal(t, "List-Unsubscribe=One-Click", headers["List-Unsubscribe-Post"])
}

func TestNotificationEmailUnsubscribeHeadersDoNotFallbackToFrontendURL(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	require.NoError(t, repo.Set(ctx, SettingKeyFrontendURL, "https://app.example.test"))
	svc := NewNotificationEmailService(repo, nil)

	variables, headers, err := svc.runtimeVariables(ctx, NotificationEmailEventBalanceLow, "en", NotificationEmailSendInput{
		RecipientEmail: "user@example.com",
	})

	require.NoError(t, err)
	require.Contains(t, variables["unsubscribe_url"], "https://app.example.test/email-unsubscribe?token=")
	require.Contains(t, headers["List-Unsubscribe"], "</api/v1/settings/email-unsubscribe?token=")
	require.NotContains(t, headers["List-Unsubscribe"], "https://app.example.test/api/v1/settings/email-unsubscribe")
	require.Equal(t, "List-Unsubscribe=One-Click", headers["List-Unsubscribe-Post"])
}

func TestNotificationEmailLocaleMemoryNormalizesAcceptLanguage(t *testing.T) {
	ctx := context.Background()
	svc := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)

	svc.RememberRecipientLocale(ctx, 42, "User@Example.com", "zh-CN,zh;q=0.9,en;q=0.8")
	require.Equal(t, "zh", svc.ResolveRecipientLocale(ctx, 42, "user@example.com"))
	require.Equal(t, "zh", svc.ResolveRecipientLocale(ctx, 0, "user@example.com"))
}

func TestNotificationEmailDeliveryKeyUsesShortStableHash(t *testing.T) {
	key := notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"User@Example.com",
		"7d",
	)
	require.NotEmpty(t, key)
	require.LessOrEqual(t, len(key), 100)
	require.True(t, strings.HasPrefix(key, notificationEmailDeliveryKeyPrefix+"v2:"))
	require.Equal(t, key, notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"7d",
	))
	require.NotEqual(t, key, notificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"3d",
	))

	legacyKey := legacyNotificationEmailDeliveryKey(
		NotificationEmailEventSubscriptionExpiryReminder,
		"user_subscription",
		"1234567890",
		"user@example.com",
		"7d",
	)
	require.Greater(t, len(legacyKey), 100)
}

func TestNotificationEmailPreferenceKeyUsesShortStableHashAndReadsLegacyKey(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)

	key := notificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "User@Example.com")
	require.NotEmpty(t, key)
	require.LessOrEqual(t, len(key), 100)
	require.True(t, strings.HasPrefix(key, notificationEmailPreferenceKeyPrefix+"v2:"))
	require.Equal(t, key, notificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "user@example.com"))

	legacyKey := legacyNotificationEmailPreferenceKey(NotificationEmailEventSubscriptionExpiryReminder, "user@example.com")
	require.Greater(t, len(legacyKey), 100)
	require.NoError(t, repo.Set(ctx, legacyKey, "unsubscribed"))

	unsubscribed, err := svc.IsUnsubscribed(ctx, "User@Example.com", NotificationEmailEventSubscriptionExpiryReminder)
	require.NoError(t, err)
	require.True(t, unsubscribed)
}

func TestNotificationEmailSendDeduplicatesSubscriptionExpiryReminder(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))

	emailSvc := NewEmailService(repo, nil)
	svc := NewNotificationEmailService(repo, emailSvc)
	input := NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: "User@Example.com",
		RecipientName:  "User",
		UserID:         42,
		SourceType:     "user_subscription",
		SourceID:       "1234567890",
		ReminderKey:    "7d",
		Variables: map[string]string{
			"subscription_group": "Codex",
			"expiry_time":        "2026-05-27 12:00",
			"days_remaining":     "7",
		},
	}

	require.NoError(t, svc.Send(ctx, input))
	require.Equal(t, int64(1), smtpServer.messageCount())
	require.Contains(t, smtpServer.lastMessage(), "List-Unsubscribe: </api/v1/settings/email-unsubscribe?token=")
	require.Contains(t, smtpServer.lastMessage(), "List-Unsubscribe-Post: List-Unsubscribe=One-Click")

	key := notificationEmailDeliveryKey(input.Event, input.SourceType, input.SourceID, input.RecipientEmail, input.ReminderKey)
	require.LessOrEqual(t, len(key), 100)
	_, err := repo.GetValue(ctx, key)
	require.NoError(t, err)

	require.NoError(t, svc.Send(ctx, input))
	require.Equal(t, int64(1), smtpServer.messageCount())
}

func TestNotificationEmailSendOnlyAddsUnsubscribeHeadersToOptionalEvents(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, repo.SetMultiple(ctx, smtpServer.settings()))
	emailSvc := NewEmailService(repo, nil)
	svc := NewNotificationEmailService(repo, emailSvc)

	require.NoError(t, svc.Send(ctx, NotificationEmailSendInput{
		Event:          NotificationEmailEventBalanceLow,
		RecipientEmail: "optional@example.com",
		Variables: map[string]string{
			"current_balance": "1.00",
			"threshold":       "5.00",
		},
	}))
	require.Contains(t, smtpServer.lastMessage(), "List-Unsubscribe:")

	require.NoError(t, svc.Send(ctx, NotificationEmailSendInput{
		Event:          NotificationEmailEventBalanceRechargeSuccess,
		RecipientEmail: "transactional@example.com",
		Variables: map[string]string{
			"recharge_amount": "10.00",
			"current_balance": "11.00",
			"order_id":        "1024",
		},
	}))
	require.NotContains(t, smtpServer.lastMessage(), "List-Unsubscribe:")
	require.NotContains(t, smtpServer.lastMessage(), "List-Unsubscribe-Post:")
}

func TestNotificationEmailSendFailsWhenOptionalUnsubscribeTokenCannotBeGenerated(t *testing.T) {
	ctx := context.Background()
	baseRepo := newNotificationEmailMemorySettingRepo()
	smtpServer := startNotificationEmailTestSMTPServer(t)
	require.NoError(t, baseRepo.SetMultiple(ctx, smtpServer.settings()))
	repo := &notificationEmailFailingSetRepo{
		notificationEmailMemorySettingRepo: baseRepo,
		failKey:                            notificationEmailUnsubscribeSecretKey,
	}
	emailSvc := NewEmailService(repo, nil)
	svc := NewNotificationEmailService(repo, emailSvc)

	err := svc.Send(ctx, NotificationEmailSendInput{
		Event:          NotificationEmailEventBalanceLow,
		RecipientEmail: "user@example.com",
		Variables: map[string]string{
			"current_balance": "1.00",
			"threshold":       "5.00",
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "generate unsubscribe token")
	require.Equal(t, int64(0), smtpServer.messageCount())
}

func TestNotificationEmailSendRespectsLegacyDeliveryKey(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	input := NotificationEmailSendInput{
		Event:          NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: "user@example.com",
		SourceType:     "user_subscription",
		SourceID:       "1234567890",
		ReminderKey:    "7d",
	}
	legacyKey := legacyNotificationEmailDeliveryKey(input.Event, input.SourceType, input.SourceID, input.RecipientEmail, input.ReminderKey)
	require.NoError(t, repo.Set(ctx, legacyKey, "sent"))

	require.NoError(t, svc.Send(ctx, input))
}

func notificationEmailBroadcastRecipientEmails(recipients []notificationEmailBroadcastRecipient) []string {
	emails := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		emails = append(emails, recipient.Email)
	}
	return emails
}

func setNotificationEmailBroadcastStatusForTest(t *testing.T, repo *notificationEmailMemorySettingRepo, status NotificationEmailBroadcastStatus) {
	t.Helper()
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	require.NoError(t, repo.Set(context.Background(), notificationEmailBroadcastKey(status.BatchID), string(raw)))
}

type notificationEmailBroadcastUserRepoStub struct {
	UserRepository
	users []User
}

func (r *notificationEmailBroadcastUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	for i := range r.users {
		if r.users[i].ID == id {
			user := r.users[i]
			return &user, nil
		}
	}
	return nil, ErrUserNotFound
}

func (r *notificationEmailBroadcastUserRepoStub) ListWithFilters(_ context.Context, params pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error) {
	matches := make([]User, 0, len(r.users))
	for _, user := range r.users {
		if filters.Role != "" && user.Role != filters.Role {
			continue
		}
		if filters.Status != "" && user.Status != filters.Status {
			continue
		}
		matches = append(matches, user)
	}
	offset := params.Offset()
	limit := params.Limit()
	if offset > len(matches) {
		offset = len(matches)
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[offset:end], &pagination.PaginationResult{
		Total:    int64(len(matches)),
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    1,
	}, nil
}

type notificationEmailMemorySettingRepo struct {
	mu     sync.RWMutex
	values map[string]string
}

type notificationEmailFailingSetRepo struct {
	*notificationEmailMemorySettingRepo
	failKey string
}

func (r *notificationEmailFailingSetRepo) Set(ctx context.Context, key, value string) error {
	if key == r.failKey {
		return errors.New("setting write failed")
	}
	return r.notificationEmailMemorySettingRepo.Set(ctx, key, value)
}

func newNotificationEmailMemorySettingRepo() *notificationEmailMemorySettingRepo {
	return &notificationEmailMemorySettingRepo{values: make(map[string]string)}
}

func (r *notificationEmailMemorySettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *notificationEmailMemorySettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *notificationEmailMemorySettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *notificationEmailMemorySettingRepo) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[key]; ok {
		return false, nil
	}
	r.values[key] = value
	return true, nil
}

func (r *notificationEmailMemorySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *notificationEmailMemorySettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *notificationEmailMemorySettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *notificationEmailMemorySettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[key]; !ok {
		return ErrSettingNotFound
	}
	delete(r.values, key)
	return nil
}

func TestNotificationEmailMemorySettingRepoSatisfiesInterface(t *testing.T) {
	var _ SettingRepository = (*notificationEmailMemorySettingRepo)(nil)
	require.False(t, strings.Contains(notificationEmailPreferenceKey(NotificationEmailEventBalanceLow, "User@Example.com"), "User@Example.com"))
}

type notificationEmailTestSMTPServer struct {
	listener net.Listener
	wg       sync.WaitGroup
	messages atomic.Int64
	mu       sync.Mutex
	last     string
}

func startNotificationEmailTestSMTPServer(t *testing.T) *notificationEmailTestSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &notificationEmailTestSMTPServer{listener: listener}
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(server.close)
	return server
}

func (s *notificationEmailTestSMTPServer) settings() map[string]string {
	host, port, _ := net.SplitHostPort(s.listener.Addr().String())
	return map[string]string{
		SettingKeySMTPHost:     host,
		SettingKeySMTPPort:     port,
		SettingKeySMTPUsername: "user",
		SettingKeySMTPPassword: "password",
		SettingKeySMTPFrom:     "noreply@example.com",
		SettingKeySMTPFromName: "Sub2API",
		SettingKeySMTPUseTLS:   "false",
	}
}

func (s *notificationEmailTestSMTPServer) messageCount() int64 {
	return s.messages.Load()
}

func (s *notificationEmailTestSMTPServer) lastMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *notificationEmailTestSMTPServer) close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

func (s *notificationEmailTestSMTPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.handleConn(conn)
	}
}

func (s *notificationEmailTestSMTPServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	writeLine := func(line string) bool {
		if _, err := rw.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return rw.Flush() == nil
	}
	if !writeLine("220 localhost ESMTP") {
		return
	}
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if _, err := rw.WriteString("250-localhost\r\n250 AUTH PLAIN\r\n"); err != nil {
				return
			}
			if err := rw.Flush(); err != nil {
				return
			}
		case strings.HasPrefix(cmd, "AUTH"):
			if !writeLine("235 2.7.0 Authentication successful") {
				return
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if !writeLine("250 2.1.0 OK") {
				return
			}
		case strings.HasPrefix(cmd, "RCPT TO:"):
			if !writeLine("250 2.1.5 OK") {
				return
			}
		case strings.HasPrefix(cmd, "DATA"):
			if !writeLine("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var message strings.Builder
			for {
				dataLine, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				_, _ = message.WriteString(dataLine)
			}
			s.mu.Lock()
			s.last = message.String()
			s.mu.Unlock()
			s.messages.Add(1)
			if !writeLine("250 2.0.0 OK") {
				return
			}
		case strings.HasPrefix(cmd, "QUIT"):
			_ = writeLine("221 2.0.0 Bye")
			return
		default:
			if !writeLine("250 OK") {
				return
			}
		}
	}
}
