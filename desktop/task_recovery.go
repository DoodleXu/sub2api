package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/imagestore"
)

const (
	taskRecoveryTimeout       = 2 * time.Minute
	taskRecoveryTaskTimeout   = 35 * time.Second
	taskRecoveryPollableLimit = 32
)

// startTaskRecovery schedules at most one recovery pass per App instance. It
// is safe to call after startup, device authorization, or an API-key switch;
// a running pass will observe the latest durable records on its next launch.
func (a *App) startTaskRecovery() {
	if a == nil {
		return
	}
	a.recoveryMu.Lock()
	if a.recoveryRunning {
		a.recoveryMu.Unlock()
		return
	}
	a.recoveryRunning = true
	a.recoveryMu.Unlock()

	ctx := a.appContext()
	go func() {
		defer func() {
			a.recoveryMu.Lock()
			a.recoveryRunning = false
			a.recoveryMu.Unlock()
		}()
		if err := a.recoverImageTasks(ctx); err != nil && !errors.Is(err, context.Canceled) {
			// Recovery is best effort. A revoked session, unavailable keyring, or
			// offline gateway must not prevent the normal UI from opening; the
			// durable checkpoint remains available for the next explicit retry.
			return
		}
	}()
}

// recoverImageTasks resumes every task owned by the current account rather
// than selecting one arbitrary row in the Vue route. Completed tasks whose
// results were not acknowledged as locally persisted are included as well.
func (a *App) recoverImageTasks(parent context.Context) error {
	if a == nil || a.tasks == nil || a.images == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, taskRecoveryTimeout)
	defer cancel()
	// Resolve the initial owner and list while the account is stable.  The
	// per-task coordinator below takes the same read lock again, so a logout or
	// account switch can never turn this initial snapshot into a request made
	// with the newly selected credentials.
	a.accountMu.RLock()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		a.accountMu.RUnlock()
		return err
	}
	records, err := a.listTasksForOwner(ctx, ownerHash)
	a.accountMu.RUnlock()
	if err != nil {
		return err
	}
	seen := 0
	for _, record := range records {
		if !recoverableTaskStatus(record.Status) && !(isSuccessfulImageTaskStatus(record.Status) && !record.AssetsDownloaded) {
			continue
		}
		if seen >= taskRecoveryPollableLimit {
			break
		}
		seen++
		taskCtx, taskCancel := context.WithTimeout(ctx, taskRecoveryTaskTimeout)
		_ = a.recoverOneImageTask(taskCtx, ownerHash, record)
		taskCancel()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func recoverableTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "processing", "pending", "queued", "in_progress", "running":
		// Empty status is treated as recoverable so a checkpoint written just
		// after a 202 response is not silently abandoned.
		return true
	default:
		return false
	}
}

func (a *App) recoverOneImageTask(ctx context.Context, ownerHash string, record imagestore.TaskRecord) error {
	if strings.TrimSpace(record.TaskID) == "" {
		return errors.New("image task id is missing")
	}
	// Re-check the owner before every network operation. This matters when a
	// user logs out and another account signs in while a background pass is in
	// flight: a stale row must never be polled with the new account's secret.
	currentOwner, err := a.currentTaskOwner(ctx)
	if err != nil {
		return err
	}
	if currentOwner != ownerHash {
		return errors.New("image task owner changed during recovery")
	}
	view := ImageTaskView{}
	// Poll until a terminal state or the per-task bound. The service owns the
	// actual generation; stopping this loop never claims to cancel that work.
	for {
		currentOwner, ownerErr := a.currentTaskOwner(ctx)
		if ownerErr != nil {
			return ownerErr
		}
		if currentOwner != ownerHash {
			return errors.New("image task owner changed during recovery")
		}
		view, err = a.getImageTaskWithContext(ctx, record.TaskID)
		if err != nil {
			return err
		}
		if isSuccessfulImageTaskStatus(view.Status) || isTerminalImageTaskStatus(view.Status) {
			break
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if !isSuccessfulImageTaskStatus(view.Status) {
		return nil
	}
	// A completed task may still need local asset persistence.  Keep the account
	// read lock for the final owner check, downloads, and checkpoint write.  This
	// prevents a concurrent account switch from placing account A's recovered
	// images in account B's visible local library or acknowledging A's row after
	// the identity transition.
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	currentOwner, ownerErr := a.currentTaskOwner(ctx)
	if ownerErr != nil {
		return ownerErr
	}
	if currentOwner != ownerHash {
		return errors.New("image task owner changed before asset recovery")
	}
	if record.AssetsDownloaded {
		return nil
	}
	if len(view.Assets) == 0 {
		// There is nothing to download, so acknowledge the terminal checkpoint and
		// avoid polling it forever on every application start.
		return a.markImageTaskAssetsDownloadedWithContext(ctx, ownerHash, record.TaskID)
	}
	allDownloaded := true
	for index, asset := range view.Assets {
		if strings.TrimSpace(asset.URL) == "" {
			allDownloaded = false
			continue
		}
		name := fmt.Sprintf("recovered-%s-%d.png", safeRecoveryTaskName(record.TaskID), index+1)
		if _, err := a.downloadImageForOwner(ctx, ownerHash, asset.URL, imagestore.AssetMetadata{Name: name}); err != nil {
			// One invalid/expired asset should not stop the remaining results. The
			// false marker causes a later explicit retry rather than data loss.
			allDownloaded = false
		}
	}
	if allDownloaded {
		return a.markImageTaskAssetsDownloadedWithContext(ctx, ownerHash, record.TaskID)
	}
	return nil
}

func isTerminalImageTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success", "failed", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}

func isSuccessfulImageTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success":
		return true
	default:
		return false
	}
}

func safeRecoveryTaskName(value string) string {
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(value))
	if len(value) > 40 {
		value = value[:40]
	}
	if value == "" {
		return "task"
	}
	return value
}
