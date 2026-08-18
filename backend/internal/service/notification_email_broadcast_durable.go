package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"
)

const (
	notificationEmailBroadcastLeaseTTL      = 2 * time.Minute
	notificationEmailBroadcastRecoveryEvery = 30 * time.Second
	notificationEmailBroadcastRetention     = 90 * 24 * time.Hour
	notificationEmailBroadcastMaxAttempts   = 3
)

func (s *NotificationEmailService) StartBroadcastWorker() {
	if s == nil || s.broadcastRepo == nil {
		return
	}
	s.broadcastWorkerOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(notificationEmailBroadcastRecoveryEvery)
			defer ticker.Stop()
			s.recoverDurableBroadcasts()
			for range ticker.C {
				s.recoverDurableBroadcasts()
			}
		}()
	})
}

func (s *NotificationEmailService) recoverDurableBroadcasts() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ids, err := s.broadcastRepo.ListRecoverable(ctx, 20)
	if err != nil {
		slog.Warn("list recoverable email broadcasts failed", "error", err)
		return
	}
	for _, id := range ids {
		go s.runDurableBroadcast(context.Background(), id)
	}
	_, _ = s.broadcastRepo.DeleteExpired(ctx, s.nowUTC().Add(-notificationEmailBroadcastRetention))
}

func (s *NotificationEmailService) startDurableBroadcast(ctx context.Context, input NotificationEmailBroadcastInput) (NotificationEmailBroadcastResult, error) {
	normalized, err := normalizeNotificationEmailBroadcastDraftInput(input, false)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if s.emailService == nil {
		return NotificationEmailBroadcastResult{}, errors.New("email service is not configured")
	}
	if _, err := s.emailService.GetSMTPConfig(ctx); err != nil {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("email service is not configured: %w", err)
	}
	recipients, err := s.resolveBroadcastRecipients(ctx, normalized)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if len(recipients) == 0 {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast requires at least one recipient")
	}
	batchID, err := notificationEmailBroadcastBatchID()
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	startedAt := s.nowUTC()
	records := make([]NotificationEmailBroadcastRecipientRecord, 0, len(recipients))
	for _, recipient := range recipients {
		parsed, parseErr := mail.ParseAddress(strings.TrimSpace(recipient.Email))
		if parseErr != nil || parsed.Address == "" {
			if normalized.Scope == "custom" {
				return NotificationEmailBroadcastResult{}, fmt.Errorf("invalid broadcast recipient %q", recipient.Email)
			}
			continue
		}
		email := strings.TrimSpace(parsed.Address)
		locale := normalized.Locale
		if locale == "auto" {
			locale = s.ResolveRecipientLocale(ctx, recipient.UserID, email)
		}
		records = append(records, NotificationEmailBroadcastRecipientRecord{
			BatchID: batchID, Email: email, NormalizedEmail: strings.ToLower(email), UserID: recipient.UserID,
			Name: recipient.Name, Locale: locale, Status: NotificationEmailBroadcastRecipientPending,
			MessageID: notificationEmailBroadcastStableMessageID(batchID, email),
		})
	}
	if len(records) == 0 {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast requires at least one valid recipient")
	}
	content := normalized.MessageTitle + "\x00" + normalized.MessageHTML + "\x00" + normalized.ActionLabel + "\x00" + normalized.ActionURL
	digest := sha256.Sum256([]byte(content))
	job := NotificationEmailBroadcastJob{
		BatchID: batchID, Status: "running", Scope: normalized.Scope, Locale: normalized.Locale,
		MessageTitle: normalized.MessageTitle, MessageHTML: normalized.MessageHTML, ActionLabel: normalized.ActionLabel,
		ActionURL: normalized.ActionURL, RPM: normalized.RPM, ContentHash: hex.EncodeToString(digest[:]),
		CreatedByUserID: normalized.CreatedByUserID, CreatedByEmail: normalized.CreatedByEmail, StartedAt: startedAt,
	}
	if err := s.broadcastRepo.Create(ctx, job, records); err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	go s.runDurableBroadcast(context.Background(), batchID)
	return NotificationEmailBroadcastResult{BatchID: batchID, TargetCount: len(records), RPM: normalized.RPM,
		EstimatedDurationSeconds: notificationEmailBroadcastEstimateSeconds(len(records), normalized.RPM), StartedAt: startedAt.Format(time.RFC3339)}, nil
}

func (s *NotificationEmailService) runDurableBroadcast(ctx context.Context, batchID string) {
	s.runDurableBroadcastWithLeaseTTL(ctx, batchID, notificationEmailBroadcastLeaseTTL)
}

func (s *NotificationEmailService) runDurableBroadcastWithLeaseTTL(ctx context.Context, batchID string, leaseTTL time.Duration) {
	ownerID, err := notificationEmailBroadcastBatchID()
	if err != nil {
		return
	}
	ownerID = "worker_" + strings.TrimPrefix(ownerID, "broadcast_")
	acquired, err := s.broadcastRepo.AcquireLease(ctx, batchID, ownerID, leaseTTL)
	if err != nil || !acquired {
		return
	}
	defer func() { _ = s.broadcastRepo.ReleaseLease(context.Background(), batchID, ownerID) }()
	workerCtx, cancelWorker := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(workerCtx)
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		defer close(heartbeatDone)
		interval := leaseTTL / 3
		if interval <= 0 {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				renewed, renewErr := s.broadcastRepo.RenewLease(heartbeatCtx, batchID, ownerID, leaseTTL)
				if renewErr == nil && renewed {
					continue
				}
				if renewErr == nil {
					renewErr = errors.New("email broadcast worker lost its lease")
				}
				select {
				case heartbeatErr <- renewErr:
				default:
				}
				cancelWorker()
				return
			}
		}
	}()
	heartbeatStopped := false
	stopAndCheckHeartbeat := func() error {
		if !heartbeatStopped {
			heartbeatStopped = true
			stopHeartbeat()
			<-heartbeatDone
		}
		select {
		case heartbeatFailure := <-heartbeatErr:
			return heartbeatFailure
		default:
			return nil
		}
	}
	defer func() {
		_ = stopAndCheckHeartbeat()
		cancelWorker()
	}()

	if err := s.broadcastRepo.MarkOrphanedSendingUncertain(workerCtx, batchID); err != nil {
		_, _ = s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "interrupted", err.Error(), true)
		return
	}
	job, err := s.broadcastRepo.Get(workerCtx, batchID)
	if err != nil {
		return
	}
	if job.CancelRequested || job.Status == "canceling" {
		_, _ = s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "canceled", "broadcast canceled by admin", true)
		return
	}
	targets, err := s.broadcastRepo.ListRunnableRecipients(workerCtx, batchID)
	if err != nil {
		_, _ = s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "interrupted", err.Error(), true)
		return
	}
	delay := time.Minute / time.Duration(job.RPM)
	for index, recipient := range targets {
		cancel, cancelErr := s.broadcastRepo.CancelRequested(workerCtx, batchID)
		if cancelErr != nil {
			if workerCtx.Err() == nil {
				_, _ = s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "interrupted", cancelErr.Error(), true)
			}
			return
		}
		if cancel {
			_, _ = s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "canceled", "broadcast canceled by admin", true)
			return
		}
		if index > 0 && !notificationEmailBroadcastWait(workerCtx, delay) {
			return
		}
		renewed, renewErr := s.broadcastRepo.RenewLease(workerCtx, batchID, ownerID, leaseTTL)
		if renewErr != nil || !renewed {
			cancelWorker()
			return
		}
		if err := s.sendDurableBroadcastRecipient(workerCtx, job, recipient); err != nil {
			slog.Warn("persist durable email broadcast recipient failed", "batch_id", batchID, "recipient", recipient.NormalizedEmail, "error", err)
			return
		}
	}
	if err := stopAndCheckHeartbeat(); err != nil {
		slog.Warn("email broadcast worker lost lease before completion", "batch_id", batchID, "error", err)
		return
	}
	renewed, err := s.broadcastRepo.RenewLease(ctx, batchID, ownerID, leaseTTL)
	if err != nil || !renewed {
		return
	}
	updated, err := s.broadcastRepo.SetJobStateIfOwned(ctx, batchID, ownerID, "completed", "", true)
	if err != nil || !updated {
		slog.Warn("complete durable email broadcast failed", "batch_id", batchID, "error", err)
	}
}

func (s *NotificationEmailService) sendDurableBroadcastRecipient(ctx context.Context, job NotificationEmailBroadcastJob, recipient NotificationEmailBroadcastRecipientRecord) error {
	for {
		attempt, claimed, err := s.broadcastRepo.ClaimRecipient(ctx, job.BatchID, recipient.NormalizedEmail)
		if err != nil {
			return fmt.Errorf("claim email broadcast recipient: %w", err)
		}
		if !claimed {
			return nil
		}
		unsubscribed, err := s.IsUnsubscribed(ctx, recipient.Email, NotificationEmailEventAdminBroadcast)
		if err == nil && unsubscribed {
			if err := s.broadcastRepo.CompleteRecipient(ctx, job.BatchID, recipient.NormalizedEmail, "skipped", "unsubscribed", "unsubscribed", nil); err != nil {
				return fmt.Errorf("persist skipped email broadcast recipient: %w", err)
			}
			return nil
		}
		if err == nil {
			err = s.Send(ctx, NotificationEmailSendInput{
				Event: NotificationEmailEventAdminBroadcast, Locale: recipient.Locale, RecipientEmail: recipient.Email,
				RecipientName: recipient.Name, UserID: recipient.UserID, SourceType: "admin_broadcast", SourceID: job.BatchID,
				Variables:        map[string]string{"message_title": job.MessageTitle, "action_label": job.ActionLabel, "action_url": job.ActionURL},
				RawHTMLVariables: map[string]string{"message_html": job.MessageHTML, "action_html": notificationEmailBroadcastActionHTML(job.ActionLabel, job.ActionURL)},
				Headers:          map[string]string{"Message-ID": recipient.MessageID, "X-Sub2API-Broadcast-ID": job.BatchID},
			})
		}
		if err == nil {
			accepted := s.nowUTC()
			if err := s.broadcastRepo.CompleteRecipient(ctx, job.BatchID, recipient.NormalizedEmail, "sent", "", "", &accepted); err != nil {
				return fmt.Errorf("persist sent email broadcast recipient: %w", err)
			}
			return nil
		}
		code, transient := notificationEmailBroadcastClassifyError(err)
		if transient && attempt < notificationEmailBroadcastMaxAttempts {
			if persistErr := s.broadcastRepo.CompleteRecipient(ctx, job.BatchID, recipient.NormalizedEmail, "retry", code, err.Error(), nil); persistErr != nil {
				return fmt.Errorf("persist retryable email broadcast recipient: %w", persistErr)
			}
			if !notificationEmailBroadcastWait(ctx, time.Duration(attempt*attempt)*5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if persistErr := s.broadcastRepo.CompleteRecipient(ctx, job.BatchID, recipient.NormalizedEmail, "failed", code, err.Error(), nil); persistErr != nil {
			return fmt.Errorf("persist failed email broadcast recipient: %w", persistErr)
		}
		return nil
	}
}

func notificationEmailBroadcastWait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func notificationEmailBroadcastClassifyError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{" 550 ", " 551 ", " 552 ", " 553 ", " 554 ", "invalid smtp recipient", "mailbox unavailable"} {
		if strings.Contains(" "+message+" ", marker) {
			return "permanent_smtp", false
		}
	}
	if strings.Contains(message, "unsubscrib") {
		return "unsubscribe_check", true
	}
	if strings.Contains(message, "auth") || strings.Contains(message, "configuration") {
		return "smtp_configuration", false
	}
	return "transient_delivery", true
}

func notificationEmailBroadcastStableMessageID(batchID, email string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(batchID) + "\x00" + strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("<broadcast.%s@sub2api.local>", hex.EncodeToString(digest[:16]))
}

func notificationEmailBroadcastStatusFromJob(job NotificationEmailBroadcastJob) NotificationEmailBroadcastStatus {
	status := NotificationEmailBroadcastStatus{BatchID: job.BatchID, Status: job.Status, Scope: job.Scope, Locale: job.Locale,
		MessageTitle: job.MessageTitle, TargetCount: job.TargetCount, SentCount: job.SentCount, SkippedCount: job.SkippedCount,
		UnsubscribedCount: job.UnsubscribedCount, FailureCount: job.FailureCount, UncertainCount: job.UncertainCount,
		CreatedByUserID: job.CreatedByUserID, CreatedByEmail: job.CreatedByEmail, RPM: job.RPM,
		StartedAt: job.StartedAt.UTC().Format(time.RFC3339), UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339), LastError: job.LastError}
	if job.CompletedAt != nil {
		status.CompletedAt = job.CompletedAt.UTC().Format(time.RFC3339)
	}
	return status
}

func (s *NotificationEmailService) listDurableBroadcasts(ctx context.Context, limit, offset int) (NotificationEmailBroadcastList, error) {
	jobs, total, err := s.broadcastRepo.List(ctx, limit, offset)
	if err != nil {
		return NotificationEmailBroadcastList{}, err
	}
	result := NotificationEmailBroadcastList{Jobs: make([]NotificationEmailBroadcastStatus, 0, len(jobs)), Total: total}
	for _, job := range jobs {
		result.Jobs = append(result.Jobs, notificationEmailBroadcastStatusFromJob(job))
		if result.ActiveBatchID == "" && (job.Status == "running" || job.Status == "canceling") {
			result.ActiveBatchID = job.BatchID
		}
	}
	return result, nil
}

func (s *NotificationEmailService) cancelDurableBroadcast(ctx context.Context, batchID string) (NotificationEmailBroadcastStatus, error) {
	job, err := s.broadcastRepo.Get(ctx, strings.TrimSpace(batchID))
	if err != nil {
		return NotificationEmailBroadcastStatus{}, err
	}
	if job.Status == "running" || job.Status == "canceling" {
		if err := s.broadcastRepo.RequestCancel(ctx, batchID); err != nil {
			return NotificationEmailBroadcastStatus{}, err
		}
		job, err = s.broadcastRepo.Get(ctx, batchID)
	}
	return notificationEmailBroadcastStatusFromJob(job), err
}

func (s *NotificationEmailService) resumeDurableBroadcast(ctx context.Context, batchID string, input NotificationEmailBroadcastResumeInput) (NotificationEmailBroadcastResult, error) {
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = "remaining"
	}
	if mode != "remaining" && mode != "failed" && mode != "uncertain" {
		return NotificationEmailBroadcastResult{}, fmt.Errorf("unsupported broadcast resume mode: %s", input.Mode)
	}
	job, err := s.broadcastRepo.Get(ctx, strings.TrimSpace(batchID))
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	if job.Status == "running" || job.Status == "canceling" {
		return NotificationEmailBroadcastResult{}, errors.New("broadcast is already running")
	}
	count, err := s.broadcastRepo.ResetRecipients(ctx, batchID, mode)
	if err != nil {
		return NotificationEmailBroadcastResult{}, err
	}
	go s.runDurableBroadcast(context.Background(), batchID)
	return NotificationEmailBroadcastResult{BatchID: batchID, TargetCount: count, RPM: job.RPM, EstimatedDurationSeconds: notificationEmailBroadcastEstimateSeconds(count, job.RPM), StartedAt: s.nowUTC().Format(time.RFC3339)}, nil
}

func (s *NotificationEmailService) getDurableBroadcastDraft(ctx context.Context) (NotificationEmailBroadcastDraft, error) {
	payload, savedAt, err := s.broadcastRepo.GetDraft(ctx, "admin")
	if err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	var input NotificationEmailBroadcastInput
	if err := json.Unmarshal(payload, &input); err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	input, err = normalizeNotificationEmailBroadcastDraftInput(input, true)
	if err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	return NotificationEmailBroadcastDraft{NotificationEmailBroadcastInput: input, SavedAt: savedAt.UTC().Format(time.RFC3339)}, nil
}

func (s *NotificationEmailService) saveDurableBroadcastDraft(ctx context.Context, input NotificationEmailBroadcastInput) (NotificationEmailBroadcastDraft, error) {
	normalized, err := normalizeNotificationEmailBroadcastDraftInput(input, true)
	if err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	savedAt, err := s.broadcastRepo.SaveDraft(ctx, "admin", payload, input.CreatedByUserID)
	if err != nil {
		return NotificationEmailBroadcastDraft{}, err
	}
	return NotificationEmailBroadcastDraft{NotificationEmailBroadcastInput: normalized, SavedAt: savedAt.UTC().Format(time.RFC3339)}, nil
}

type NotificationEmailBroadcastPreflight struct {
	TargetCount              int            `json:"target_count"`
	ValidCount               int            `json:"valid_count"`
	InvalidCount             int            `json:"invalid_count"`
	UnsubscribedCount        int            `json:"unsubscribed_count"`
	EstimatedDurationSeconds int            `json:"estimated_duration_seconds"`
	SampleEmails             []string       `json:"sample_emails"`
	Domains                  map[string]int `json:"domains"`
}

func (s *NotificationEmailService) PreflightBroadcast(ctx context.Context, input NotificationEmailBroadcastInput) (NotificationEmailBroadcastPreflight, error) {
	normalized, err := normalizeNotificationEmailBroadcastDraftInput(input, false)
	if err != nil {
		return NotificationEmailBroadcastPreflight{}, err
	}
	recipients, err := s.resolveBroadcastRecipients(ctx, normalized)
	if err != nil {
		return NotificationEmailBroadcastPreflight{}, err
	}
	result := NotificationEmailBroadcastPreflight{TargetCount: len(recipients), Domains: map[string]int{}}
	for _, recipient := range recipients {
		parsed, parseErr := mail.ParseAddress(recipient.Email)
		if parseErr != nil {
			result.InvalidCount++
			continue
		}
		result.ValidCount++
		if len(result.SampleEmails) < 5 {
			result.SampleEmails = append(result.SampleEmails, maskNotificationEmailAddress(parsed.Address))
		}
		if at := strings.LastIndex(parsed.Address, "@"); at >= 0 {
			result.Domains[strings.ToLower(parsed.Address[at+1:])]++
		}
		unsubscribed, checkErr := s.IsUnsubscribed(ctx, parsed.Address, NotificationEmailEventAdminBroadcast)
		if checkErr != nil {
			return NotificationEmailBroadcastPreflight{}, checkErr
		}
		if unsubscribed {
			result.UnsubscribedCount++
		}
	}
	result.EstimatedDurationSeconds = notificationEmailBroadcastEstimateSeconds(result.ValidCount-result.UnsubscribedCount, normalized.RPM)
	return result, nil
}

func maskNotificationEmailAddress(value string) string {
	parts := strings.SplitN(value, "@", 2)
	if len(parts) != 2 {
		return "***"
	}
	local := parts[0]
	if len(local) > 2 {
		local = local[:1] + "***" + local[len(local)-1:]
	} else {
		local = "***"
	}
	return local + "@" + parts[1]
}

func (s *NotificationEmailService) ListBroadcastRecipients(ctx context.Context, batchID, status string, page, pageSize int) (NotificationEmailBroadcastRecipientPage, error) {
	if s == nil || s.broadcastRepo == nil {
		return NotificationEmailBroadcastRecipientPage{}, errors.New("durable email broadcast storage is not configured")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	result, err := s.broadcastRepo.ListRecipients(ctx, strings.TrimSpace(batchID), strings.TrimSpace(status), pageSize, (page-1)*pageSize)
	if err != nil {
		return NotificationEmailBroadcastRecipientPage{}, err
	}
	for index := range result.Recipients {
		masked := maskNotificationEmailAddress(result.Recipients[index].Email)
		result.Recipients[index].Email = masked
		result.Recipients[index].NormalizedEmail = masked
	}
	return result, nil
}
