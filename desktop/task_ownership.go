package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/desktop/internal/imagestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/siteclient"
)

var (
	errTaskOwnershipUnsupported  = errors.New("本地图片任务存储不支持账户隔离")
	errImageOwnershipUnsupported = errors.New("本地图片存储不支持账户隔离")
)

// currentTaskOwner resolves the partition used by local image checkpoints.
// Device sessions are keyed by the server-confirmed account id (obtained from
// /user/profile); API-key-only sessions use the selected key identity because
// an API key does not expose a user subject to the native client. In both
// cases only a domain-separated digest is persisted, never the raw subject or
// key secret. A missing subject fails closed rather than sharing old rows.
func (a *App) currentTaskOwner(ctx context.Context) (string, error) {
	if a == nil || a.config == nil || a.secrets == nil {
		return "", imagestore.ErrTaskOwnerRequired
	}
	config, err := a.config.Load(ctx)
	if err != nil {
		return "", err
	}
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	deviceMode := isUsableDeviceSessionConfig(config)
	if !deviceMode && (mode == "device" || (mode == "" && strings.TrimSpace(config.RefreshTokenRef) != "")) {
		return "", errors.New("设备会话配置无效，拒绝恢复本地图片任务")
	}
	if deviceMode {
		profile, err := runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.AccountProfile, error) {
			return client.Profile(ctx, token)
		})
		if err != nil {
			return "", fmt.Errorf("无法确认当前账户身份，拒绝恢复本地图片任务: %w", err)
		}
		if profile.ID <= 0 {
			return "", errors.New("服务端未返回有效账户身份，拒绝恢复本地图片任务")
		}
		owner := imagestore.OwnerHashForSubject(fmt.Sprintf("user:%d", profile.ID))
		if owner == "" {
			return "", imagestore.ErrTaskOwnerRequired
		}
		// The profile request may cold-start a refresh rotation. That path can
		// update the nonce, refresh references, and other session metadata while
		// this function is waiting. Reload before writing the owner digest so a
		// stale snapshot can never overwrite the newly rotated session state.
		latest, loadErr := a.config.Load(ctx)
		if loadErr != nil {
			return "", loadErr
		}
		if !isUsableDeviceSessionConfig(latest) {
			return "", errors.New("设备会话在确认账户身份后已失效，拒绝恢复本地图片任务")
		}
		if latest.AccountOwnerHash != owner {
			latest.AccountOwnerHash = owner
			if saveErr := a.config.Save(ctx, latest); saveErr != nil {
				return "", saveErr
			}
		}
		return owner, nil
	}

	// API-key mode has no account bearer subject. Require the selected secret to
	// still exist before trusting the persisted key id; otherwise a stale or
	// tampered config could expose the previous account's local assets while the
	// current account is actually disconnected.
	if config.APIKeyID > 0 {
		keyRef := strings.TrimSpace(config.APIKeyRef)
		// An empty reference means the connection was cleared or never committed.
		// Do not fall back to the fixed keyring slot: that slot can contain an
		// orphaned secret after a credential-manager deletion failure.
		if keyRef != apiKeyRef {
			return "", errors.New("API key 引用无效，拒绝恢复本地图片任务")
		}
		if key, secretErr := a.secrets.Get(ctx, keyRef); secretErr != nil || strings.TrimSpace(key) == "" {
			return "", imagestore.ErrTaskOwnerRequired
		}
		return imagestore.OwnerHashForSubject(fmt.Sprintf("api-key-id:%d", config.APIKeyID)), nil
	}
	keyRef := strings.TrimSpace(config.APIKeyRef)
	if keyRef != apiKeyRef {
		return "", errors.New("API key 引用无效，拒绝恢复本地图片任务")
	}
	key, err := a.secrets.Get(ctx, keyRef)
	if err != nil || strings.TrimSpace(key) == "" {
		return "", imagestore.ErrTaskOwnerRequired
	}
	return imagestore.OwnerHashForSubject("api-key-secret:" + key), nil
}

func (a *App) putTaskForOwner(ctx context.Context, ownerHash string, task imagestore.TaskRecord) error {
	store, ok := a.tasks.(imagestore.ScopedTaskStore)
	if !ok || store == nil {
		return errTaskOwnershipUnsupported
	}
	return store.PutForOwner(ctx, ownerHash, task)
}

func (a *App) getTaskForOwner(ctx context.Context, ownerHash, taskID string) (imagestore.TaskRecord, error) {
	store, ok := a.tasks.(imagestore.ScopedTaskStore)
	if !ok || store == nil {
		return imagestore.TaskRecord{}, errTaskOwnershipUnsupported
	}
	return store.GetForOwner(ctx, ownerHash, taskID)
}

func (a *App) listTasksForOwner(ctx context.Context, ownerHash string) ([]imagestore.TaskRecord, error) {
	store, ok := a.tasks.(imagestore.ScopedTaskStore)
	if !ok || store == nil {
		return nil, errTaskOwnershipUnsupported
	}
	return store.ListForOwner(ctx, ownerHash)
}

func (a *App) imageStoreForOwner() (imagestore.ScopedStore, error) {
	if a == nil || a.images == nil {
		return nil, errImageOwnershipUnsupported
	}
	store, ok := a.images.(imagestore.ScopedStore)
	if !ok || store == nil {
		return nil, errImageOwnershipUnsupported
	}
	return store, nil
}

func (a *App) downloadImageForOwner(ctx context.Context, ownerHash, sourceURL string, metadata imagestore.AssetMetadata) (imagestore.Asset, error) {
	store, err := a.imageStoreForOwner()
	if err != nil {
		return imagestore.Asset{}, err
	}
	return store.DownloadForOwner(ctx, ownerHash, sourceURL, nil, metadata)
}

var _ imagestore.ScopedTaskStore = (*imagestore.SQLiteTaskStore)(nil)
var _ imagestore.ScopedTaskStore = (*imagestore.JSONTaskStore)(nil)
