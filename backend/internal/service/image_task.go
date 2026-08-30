package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	ImageTaskStatusProcessing = "processing"
	ImageTaskStatusCompleted  = "completed"
	ImageTaskStatusFailed     = "failed"

	defaultImageTaskTTL              = 24 * time.Hour
	defaultImageTaskExecutionTimeout = 30 * time.Minute
	imageTaskReconcileTimeout        = 5 * time.Second
	imageTaskCleanupPollInterval     = time.Minute
	imageTaskCleanupBatchSize        = 100
)

var (
	ErrImageTaskNotFound    = infraerrors.New(http.StatusNotFound, "IMAGE_TASK_NOT_FOUND", "image task not found")
	ErrImageTaskForbidden   = infraerrors.New(http.StatusForbidden, "IMAGE_TASK_FORBIDDEN", "image task does not belong to this API key")
	ErrImageTaskUnavailable = infraerrors.New(http.StatusServiceUnavailable, "IMAGE_TASK_UNAVAILABLE", "image task storage is unavailable")
	// User history can be removed only after the asynchronous execution reaches
	// a terminal state.  Rejecting a live task avoids racing a late completion
	// transition that could otherwise recreate the just-deleted history row.
	ErrImageTaskDeleteNotReady = infraerrors.New(http.StatusConflict, "IMAGE_TASK_DELETE_NOT_READY", "image task can only be deleted after it finishes")
	// ErrImageTaskAssetsExpired deliberately uses 410 rather than 404 so a
	// client can distinguish a valid historical task from an asset whose
	// short-lived access grant has elapsed.
	ErrImageTaskAssetsExpired = infraerrors.New(http.StatusGone, "IMAGE_TASK_ASSETS_EXPIRED", "image task assets have expired")
)

type imageTaskCompletionReconcileResult int

const (
	imageTaskCompletionUnknown imageTaskCompletionReconcileResult = iota
	imageTaskCompletionCommitted
	imageTaskCompletionNotCommitted
)

// ImageTaskRecord is the private execution and history representation of an
// asynchronous image request. Ownership fields are intentionally omitted from
// the public view.
type ImageTaskRecord struct {
	ID                string          `json:"id"`
	UserID            int64           `json:"user_id"`
	APIKeyID          int64           `json:"api_key_id"`
	Platform          string          `json:"platform,omitempty"`
	Operation         string          `json:"operation,omitempty"`
	Model             string          `json:"model,omitempty"`
	ImageCount        int             `json:"image_count,omitempty"`
	Status            string          `json:"status"`
	HTTPStatus        int             `json:"http_status,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
	PendingObjectKeys []string        `json:"pending_object_keys,omitempty"`
	// ResultObjectKeys are the durable object identities behind Result URLs.
	// They stay private and let the admin history endpoint issue fresh presigned
	// URLs after the URL captured at task completion has expired.
	ResultObjectKeys []string `json:"result_object_keys,omitempty"`
	// StorageBindingID identifies the object-store configuration used for this
	// task. It is a non-secret fingerprint, never a credential.
	StorageBindingID string `json:"storage_binding_id,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	CompletedAt      *int64 `json:"completed_at,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
}

// ImageTask is the API-safe task representation returned to callers.
type ImageTask struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	Object      string          `json:"object"`
	Status      string          `json:"status"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	ImageURL    string          `json:"image_url,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt *int64          `json:"completed_at,omitempty"`
	ExpiresAt   int64           `json:"expires_at"`
}

type ImageTaskOwner struct {
	UserID   int64
	APIKeyID int64
}

type ImageTaskMetadata struct {
	Platform   string
	Operation  string
	Model      string
	ImageCount int
}

type ImageTaskAdminQuery struct {
	Status  string
	Cursor  string
	Limit   int
	StartAt int64
	EndAt   int64
}

type ImageTaskAdminStats struct {
	Processing int `json:"processing"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
}

type ImageTaskAdminPage struct {
	Tasks      []*ImageTaskRecord
	NextCursor string
	HasMore    bool
	Stats      ImageTaskAdminStats
}

// ImageTaskUserQuery is the authenticated user's history filter. UserID is
// always populated from the JWT subject by the handler; it is never accepted
// from query parameters, preventing horizontal history enumeration.
type ImageTaskUserQuery struct {
	UserID  int64
	Status  string
	Cursor  string
	Limit   int
	StartAt int64
	EndAt   int64
}

type ImageTaskUserPage struct {
	Tasks      []*ImageTaskRecord
	NextCursor string
	HasMore    bool
}

type ImageTaskAdminStore interface {
	ListAdmin(ctx context.Context, query ImageTaskAdminQuery) (*ImageTaskAdminPage, error)
}

// ImageTaskUserStore is an optional durable-history capability. Keeping it
// separate from ImageTaskStore preserves compatibility with lightweight Redis
// test implementations and makes the user-facing history endpoint fail closed
// when persistence is not wired.
type ImageTaskUserStore interface {
	ListUser(ctx context.Context, query ImageTaskUserQuery) (*ImageTaskUserPage, error)
	GetUser(ctx context.Context, userID int64, id string) (*ImageTaskRecord, error)
}

// ImageTaskUserDeleteStore is optional so existing lightweight stores remain
// source-compatible. Implementations must enforce the same user predicate as
// GetUser and remove both durable history and hot Redis state.
type ImageTaskUserDeleteStore interface {
	DeleteUser(ctx context.Context, userID int64, id string) error
}

type imageTaskAdminCursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

type ImageTaskStore interface {
	// 所有方法必须响应 ctx 取消。ListPending 是对象存储清理的强制持久扫描能力，
	// 避免自定义实现静默退化为只能依赖客户端轮询。
	Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error
	Get(ctx context.Context, id string) (*ImageTaskRecord, error)
	// Transition 原子地把 expectedStatus 终态转换为 task；状态已变化时返回 false。
	Transition(ctx context.Context, id, expectedStatus string, task *ImageTaskRecord, ttl time.Duration) (bool, error)
	ListPending(ctx context.Context, limit int) ([]*ImageTaskRecord, error)
}

// ImageStorageResolver reports the currently effective object-storage binding.
// It exists so the async image feature can be switched on and off from the admin
// UI without a restart: the wiring below is fixed at startup, but the answer to
// "is object storage configured right now" is re-read (and cached) per call.
type ImageStorageResolver func() (uploader *ImageResultUploader, enabled bool)

type ImageTaskService struct {
	store            ImageTaskStore
	uploader         *ImageResultUploader
	enabled          bool
	resolve          ImageStorageResolver
	ttl              time.Duration
	executionTimeout time.Duration
	cleanupMu        sync.Mutex
	cleanupClosed    bool
	cleanupRunning   map[string]struct{}
	cleanupSlots     chan struct{}
	cleanupStop      chan struct{}
	cleanupStopOnce  sync.Once
	cleanupCancel    context.CancelFunc
	cleanupCtx       context.Context
	cleanupWG        sync.WaitGroup
	taskUploaders    sync.Map // task ID -> uploader snapshot captured at admission
}

func EncodeImageTaskAdminCursor(createdAt int64, id string) string {
	payload, _ := json.Marshal(imageTaskAdminCursor{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeImageTaskAdminCursor(value string) (createdAt int64, id string, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, "", nil
	}
	payload, decodeErr := base64.RawURLEncoding.DecodeString(value)
	if decodeErr != nil {
		return 0, "", infraerrors.BadRequest("INVALID_IMAGE_TASK_CURSOR", "invalid image task cursor")
	}
	var cursor imageTaskAdminCursor
	if unmarshalErr := json.Unmarshal(payload, &cursor); unmarshalErr != nil || cursor.CreatedAt < 0 || strings.TrimSpace(cursor.ID) == "" {
		return 0, "", infraerrors.BadRequest("INVALID_IMAGE_TASK_CURSOR", "invalid image task cursor")
	}
	return cursor.CreatedAt, cursor.ID, nil
}

func NewImageTaskService(store ImageTaskStore) *ImageTaskService {
	return NewImageTaskServiceWithOptions(store, defaultImageTaskTTL, defaultImageTaskExecutionTimeout)
}

func NewImageTaskServiceWithOptions(store ImageTaskStore, ttl, executionTimeout time.Duration) *ImageTaskService {
	if ttl <= 0 {
		ttl = defaultImageTaskTTL
	}
	if executionTimeout <= 0 {
		executionTimeout = defaultImageTaskExecutionTimeout
	}
	s := &ImageTaskService{
		store:            store,
		ttl:              ttl,
		executionTimeout: executionTimeout,
		cleanupRunning:   make(map[string]struct{}),
		cleanupSlots:     make(chan struct{}, 8),
		cleanupStop:      make(chan struct{}),
	}
	s.cleanupCtx, s.cleanupCancel = context.WithCancel(context.Background())
	return s
}

// NewImageTaskServiceWithUploader 构造一个已启用的图片任务服务：结果会先经 uploader
// 转存到对象存储再落 Redis。uploader 为 nil 时不做转存（仅用于测试）。
func NewImageTaskServiceWithUploader(store ImageTaskStore, uploader *ImageResultUploader, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.uploader = uploader
	s.enabled = true
	if uploader != nil && uploader.storage == nil {
		// A configured async service must never fall back to persisting raw base64
		// in Redis when the object store dependency is missing.
		s.enabled = false
		return s
	}
	if uploader != nil {
		s.cleanupWG.Add(1)
		go func() {
			defer s.cleanupWG.Done()
			s.pendingObjectCleanupWorker()
		}()
	}
	return s
}

// NewImageTaskServiceWithResolver 构造一个由 resolver 决定启用状态的服务：
// 开关与凭证来自后台设置，保存后立即生效，无需重启。
func NewImageTaskServiceWithResolver(store ImageTaskStore, resolve ImageStorageResolver, ttl, executionTimeout time.Duration) *ImageTaskService {
	s := NewImageTaskServiceWithOptions(store, ttl, executionTimeout)
	s.resolve = resolve
	if resolve != nil {
		s.cleanupWG.Add(1)
		go func() {
			defer s.cleanupWG.Done()
			s.pendingObjectCleanupWorker()
		}()
	}
	return s
}

// current 返回当前生效的 uploader 与启用状态。
// 注入了 resolver 时以 resolver 为准（后台设置可热切换），否则回落到构造时固定的值。
func (s *ImageTaskService) current() (*ImageResultUploader, bool) {
	if s == nil {
		return nil, false
	}
	if s.resolve != nil {
		return s.resolve()
	}
	return s.uploader, s.enabled
}

func (s *ImageTaskService) uploaderForTaskRecord(task *ImageTaskRecord) *ImageResultUploader {
	if s == nil || task == nil {
		return nil
	}
	if uploader, ok := s.taskUploaders.Load(task.ID); ok {
		if typed, valid := uploader.(*ImageResultUploader); valid {
			if task.StorageBindingID == "" || typed.BindingID() == task.StorageBindingID {
				return typed
			}
			return nil
		}
	}
	// A resolver may return a new bucket after a restart or settings change.
	// Without a persisted binding, never use that current uploader for a task
	// carrying durable object identities; it could sign or delete another store's
	// objects. Legacy processing tasks without object keys can still complete.
	if s.resolve != nil && task.StorageBindingID == "" && (len(task.PendingObjectKeys) > 0 || len(task.ResultObjectKeys) > 0) {
		return nil
	}
	uploader, _ := s.current()
	if uploader == nil {
		return nil
	}
	if task.StorageBindingID != "" && uploader.BindingID() != task.StorageBindingID {
		return nil
	}
	return uploader
}

func (s *ImageTaskService) forgetTaskUploader(id string) {
	if s != nil {
		s.taskUploaders.Delete(id)
	}
}

// Close stops background cleanup. It is safe to call more than once.
func (s *ImageTaskService) Close() {
	if s == nil {
		return
	}
	s.cleanupStopOnce.Do(func() { close(s.cleanupStop) })
	s.cleanupMu.Lock()
	s.cleanupClosed = true
	s.cleanupMu.Unlock()
	if s.cleanupCancel != nil {
		s.cleanupCancel()
	}
	s.cleanupWG.Wait()
}

func (s *ImageTaskService) pendingObjectCleanupWorker() {
	ticker := time.NewTicker(imageTaskCleanupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.reconcilePendingObjects()
		case <-s.cleanupStop:
			return
		}
	}
}

func (s *ImageTaskService) reconcilePendingObjects() {
	if s.store == nil {
		return
	}
	parent := s.cleanupCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, imageStorageCleanupTimeout+imageTaskReconcileTimeout)
	defer cancel()
	tasks, err := s.store.ListPending(ctx, imageTaskCleanupBatchSize)
	if err != nil {
		logger.L().Warn("image_task.pending_object_scan_failed", zap.Error(err))
		return
	}
	now := time.Now().Unix()
	for _, task := range tasks {
		if task == nil {
			continue
		}
		createdAt := time.Unix(task.CreatedAt, 0)
		if task.Status == ImageTaskStatusProcessing && (task.CreatedAt <= 0 || time.Since(createdAt) >= s.ExecutionTimeout()) {
			failed := *task
			failed.Status = ImageTaskStatusFailed
			failed.HTTPStatus = http.StatusGatewayTimeout
			failed.Error = imageTaskErrorJSON("timeout", "image task execution timed out")
			completedAt := now
			failed.CompletedAt = &completedAt
			failed.ExpiresAt = time.Now().Add(s.ttl).Unix()
			transitioned, transitionErr := s.store.Transition(ctx, task.ID, ImageTaskStatusProcessing, &failed, s.ttl)
			if transitionErr != nil || !transitioned {
				continue
			}
			task = &failed
		}
		if task.Status == ImageTaskStatusFailed && len(task.PendingObjectKeys) > 0 {
			uploader := s.uploaderForTaskRecord(task)
			if uploader == nil {
				continue
			}
			if err := s.cleanupFailedPendingObjects(ctx, task, uploader); err != nil {
				logger.L().Warn("image_task.pending_object_cleanup_failed", zap.String("task_id", task.ID), zap.Error(err))
			} else {
				s.forgetTaskUploader(task.ID)
			}
		} else if task.Status == ImageTaskStatusFailed {
			// A task that timed out before uploading anything has no compensation
			// work left. Release its admission-time uploader snapshot immediately.
			s.forgetTaskUploader(task.ID)
		}
	}
}

// Enabled 表示异步图片任务功能是否可用（总开关 + 凭证齐全）。
// 关闭时 handler 直接返回 404，不创建任务、不写 Redis。
func (s *ImageTaskService) Enabled() bool {
	if s == nil || s.store == nil {
		return false
	}
	_, enabled := s.current()
	return enabled
}

// Pollable 表示已创建的任务能否被查询。
// 比 Enabled 弱：只要 store 可用即可，从而在功能被关掉后仍能取回进行中的任务结果。
func (s *ImageTaskService) Pollable() bool {
	return s != nil && s.store != nil
}

func (s *ImageTaskService) ExecutionTimeout() time.Duration {
	if s == nil || s.executionTimeout <= 0 {
		return defaultImageTaskExecutionTimeout
	}
	return s.executionTimeout
}

func (s *ImageTaskService) Create(ctx context.Context, owner ImageTaskOwner, metadata ...ImageTaskMetadata) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	uploader, enabled := s.current()
	if s.resolve != nil && (!enabled || uploader == nil) {
		return nil, ErrImageTaskUnavailable
	}
	now := time.Now().UTC()
	task := &ImageTaskRecord{
		ID:        "imgtask_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		UserID:    owner.UserID,
		APIKeyID:  owner.APIKeyID,
		Status:    ImageTaskStatusProcessing,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(s.ttl).Unix(),
	}
	if len(metadata) > 0 {
		task.Platform = strings.TrimSpace(metadata[0].Platform)
		task.Operation = strings.TrimSpace(metadata[0].Operation)
		task.Model = strings.TrimSpace(metadata[0].Model)
		task.ImageCount = metadata[0].ImageCount
	}
	if uploader != nil {
		task.StorageBindingID = uploader.BindingID()
	}
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	if uploader != nil {
		s.taskUploaders.Store(task.ID, uploader)
	}
	return imageTaskToPublic(task), nil
}

func (s *ImageTaskService) ListAdmin(ctx context.Context, query ImageTaskAdminQuery) (*ImageTaskAdminPage, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	store, ok := s.store.(ImageTaskAdminStore)
	if !ok {
		return nil, ErrImageTaskUnavailable
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	query.Status = strings.TrimSpace(query.Status)
	if query.Status != "" && query.Status != "all" && query.Status != ImageTaskStatusProcessing && query.Status != ImageTaskStatusCompleted && query.Status != ImageTaskStatusFailed {
		return nil, infraerrors.BadRequest("INVALID_IMAGE_TASK_STATUS", "invalid image task status")
	}
	if query.StartAt < 0 || query.EndAt < 0 || (query.StartAt > 0 && query.EndAt > 0 && query.StartAt >= query.EndAt) {
		return nil, infraerrors.BadRequest("INVALID_IMAGE_TASK_DATE_RANGE", "invalid image task date range")
	}
	if _, _, err := DecodeImageTaskAdminCursor(query.Cursor); err != nil {
		return nil, err
	}
	page, err := store.ListAdmin(ctx, query)
	if err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	return page, nil
}

// ListUser returns only tasks owned by userID. The repository implementation
// applies the same predicate in SQL/Redis; the service also rejects a missing
// identity so a caller can never accidentally request a global history page.
func (s *ImageTaskService) ListUser(ctx context.Context, query ImageTaskUserQuery) (*ImageTaskUserPage, error) {
	if s == nil || s.store == nil || query.UserID <= 0 {
		return nil, ErrImageTaskUnavailable
	}
	store, ok := s.store.(ImageTaskUserStore)
	if !ok {
		return nil, ErrImageTaskUnavailable
	}
	if query.Limit <= 0 {
		query.Limit = 50
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	query.Status = strings.TrimSpace(query.Status)
	if query.Status != "" && query.Status != "all" && query.Status != ImageTaskStatusProcessing && query.Status != ImageTaskStatusCompleted && query.Status != ImageTaskStatusFailed {
		return nil, infraerrors.BadRequest("INVALID_IMAGE_TASK_STATUS", "invalid image task status")
	}
	if query.StartAt < 0 || query.EndAt < 0 || (query.StartAt > 0 && query.EndAt > 0 && query.StartAt >= query.EndAt) {
		return nil, infraerrors.BadRequest("INVALID_IMAGE_TASK_DATE_RANGE", "invalid image task date range")
	}
	if _, _, err := DecodeImageTaskAdminCursor(query.Cursor); err != nil {
		return nil, err
	}
	page, err := store.ListUser(ctx, query)
	if err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	return page, nil
}

// GetUser reads durable history by JWT user ownership, independent of the API
// key that originally admitted the task. This is what lets a desktop client
// show one coherent history after the user rotates or revokes an API key.
func (s *ImageTaskService) GetUser(ctx context.Context, userID int64, id string) (*ImageTaskRecord, error) {
	if s == nil || s.store == nil || userID <= 0 {
		return nil, ErrImageTaskUnavailable
	}
	store, ok := s.store.(ImageTaskUserStore)
	if !ok {
		return nil, ErrImageTaskUnavailable
	}
	task, err := store.GetUser(ctx, userID, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return nil, ErrImageTaskNotFound
		}
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	if task == nil || task.UserID != userID {
		return nil, ErrImageTaskNotFound
	}
	s.scheduleFailedPendingObjectCleanup(task)
	return task, nil
}

// DeleteUser removes one terminal task from the authenticated user's history.
// Object identities are deleted before the database/Redis manifest so a
// storage failure does not strand an inaccessible row or leak an orphaned
// object. The operation is intentionally terminal-only: a processing task may
// still race a detached completion transition and is therefore a 409.
func (s *ImageTaskService) DeleteUser(ctx context.Context, userID int64, id string) error {
	if s == nil || s.store == nil || userID <= 0 {
		return ErrImageTaskUnavailable
	}
	store, ok := s.store.(ImageTaskUserDeleteStore)
	if !ok {
		return ErrImageTaskUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrImageTaskNotFound
	}
	task, err := s.GetUser(ctx, userID, id)
	if err != nil {
		return err
	}
	if task == nil || task.UserID != userID {
		return ErrImageTaskNotFound
	}
	if task.Status != ImageTaskStatusCompleted && task.Status != ImageTaskStatusFailed {
		return ErrImageTaskDeleteNotReady
	}
	keys := append([]string(nil), task.ResultObjectKeys...)
	keys = append(keys, task.PendingObjectKeys...)
	if len(keys) > 0 {
		uploader := s.uploaderForTaskRecord(task)
		if uploader == nil {
			// Do not remove the manifest when the binding used to create its
			// objects is unavailable; an operator can restore that binding and
			// retry deletion without losing the object identities.
			return ErrImageTaskUnavailable
		}
		if err := uploader.DeleteKeys(ctx, keys); err != nil {
			return ErrImageTaskUnavailable.WithCause(err)
		}
	}
	if err := store.DeleteUser(ctx, userID, id); err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return ErrImageTaskNotFound
		}
		if errors.Is(err, ErrImageTaskDeleteNotReady) {
			return ErrImageTaskDeleteNotReady
		}
		return ErrImageTaskUnavailable.WithCause(err)
	}
	s.forgetTaskUploader(id)
	return nil
}

// ResolveResultURLs issues fresh object-store URLs for a historical task. It
// never returns ResultObjectKeys to callers. ExpiresAt is the short runtime
// lease for processing tasks; once a task is completed/failed, its durable
// history remains readable until the user deletes it (or the configured object
// store lifecycle removes the physical object).
func (s *ImageTaskService) ResolveResultURLs(ctx context.Context, task *ImageTaskRecord) ([]string, error) {
	if s == nil || task == nil {
		return nil, ErrImageTaskNotFound
	}
	if task.Status == ImageTaskStatusProcessing && task.ExpiresAt > 0 && time.Now().Unix() >= task.ExpiresAt {
		return nil, ErrImageTaskAssetsExpired
	}
	if len(task.ResultObjectKeys) == 0 {
		return imageTaskResultURLs(task.Result), nil
	}
	uploader := s.uploaderForTaskRecord(task)
	if uploader == nil {
		return nil, ErrImageTaskUnavailable
	}
	urls, err := uploader.ResolveURLs(ctx, task.ResultObjectKeys)
	if err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	ordered := make([]string, 0, len(task.ResultObjectKeys))
	for _, key := range task.ResultObjectKeys {
		value := strings.TrimSpace(urls[strings.TrimSpace(key)])
		if value == "" {
			return nil, ErrImageTaskUnavailable
		}
		ordered = append(ordered, value)
	}
	return ordered, nil
}

func (s *ImageTaskService) Get(ctx context.Context, owner ImageTaskOwner, id string) (*ImageTask, error) {
	if s == nil || s.store == nil {
		return nil, ErrImageTaskUnavailable
	}
	task, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return nil, ErrImageTaskNotFound
		}
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	if task.UserID != owner.UserID || task.APIKeyID != owner.APIKeyID {
		// Do not reveal whether a random task ID exists for another caller.
		return nil, ErrImageTaskNotFound
	}
	s.scheduleFailedPendingObjectCleanup(task)
	return imageTaskToPublic(task), nil
}

func (s *ImageTaskService) Complete(ctx context.Context, id string, statusCode int, result json.RawMessage) error {
	if !json.Valid(result) {
		return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "upstream returned a non-JSON image response"))
	}
	task, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return ErrImageTaskNotFound
		}
		return ErrImageTaskUnavailable.WithCause(err)
	}
	uploader := s.uploaderForTaskRecord(task)
	if uploader == nil && (s.resolve != nil || task.StorageBindingID != "") {
		logger.L().Error("image_task.uploader_snapshot_unavailable", zap.String("task_id", id))
		return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "image object storage is unavailable"))
	}
	var uploadedKeys []string
	if uploader != nil {
		rewritten, err := uploader.rewriteTracked(ctx, id, result, func(keys []string) (bool, error) {
			return s.trackPendingObjects(ctx, id, keys)
		})
		if err != nil {
			// 转存失败不回退存 base64，避免大 blob 撑爆 Redis：直接把任务标记为失败。
			logger.L().Error("image_task.offload_failed", zap.String("task_id", id), zap.Error(err))
			return s.Fail(ctx, id, http.StatusBadGateway, imageTaskErrorJSON("api_error", "failed to store generated image to object storage"))
		}
		if !rewritten.active {
			s.forgetTaskUploader(id)
			return nil
		}
		result = rewritten.payload
		uploadedKeys = rewritten.keys
	}
	transitioned, transitionAttempted, err := s.finish(ctx, id, ImageTaskStatusCompleted, statusCode, result, nil, uploadedKeys)
	cleanupUploaded := err == nil && !transitioned
	if err != nil && transitionAttempted {
		switch s.reconcileCompletionAfterTransitionError(id, result) {
		case imageTaskCompletionCommitted:
			s.forgetTaskUploader(id)
			return nil
		case imageTaskCompletionNotCommitted:
			cleanupUploaded = true
			// Reconciliation found an irreversible terminal state. This completion
			// no longer needs handler-level failure fallback after its objects are
			// cleaned up.
			err = nil
		case imageTaskCompletionUnknown:
			cleanupUploaded = false
		}
	} else if err != nil {
		// The task could not be loaded, so no completion CAS was sent and this
		// attempt's unique objects cannot have been referenced by the task.
		cleanupUploaded = true
	}
	if cleanupUploaded && len(uploadedKeys) > 0 {
		if cleanupErr := uploader.cleanup(uploadedKeys); cleanupErr != nil {
			logger.L().Error("image_task.offload_cleanup_failed", zap.String("task_id", id), zap.Error(cleanupErr))
		} else {
			s.forgetTaskUploader(id)
		}
	} else if err == nil && transitioned {
		s.forgetTaskUploader(id)
	}
	return err
}

// reconcileCompletionAfterTransitionError reconciles an ambiguous Redis
// transition. A command may have committed before its caller observes a
// deadline or network error, so deleting the uploaded objects solely because
// Transition returned an error could break an already-completed task.
func (s *ImageTaskService) reconcileCompletionAfterTransitionError(id string, candidate json.RawMessage) imageTaskCompletionReconcileResult {
	if s == nil || s.store == nil {
		return imageTaskCompletionUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageTaskReconcileTimeout)
	defer cancel()
	task, err := s.store.Get(ctx, id)
	if err != nil {
		logger.L().Warn("image_task.completion_reconcile_failed_preserving_objects", zap.String("task_id", id), zap.Error(err))
		return imageTaskCompletionUnknown
	}
	if task.Status == ImageTaskStatusCompleted && bytes.Equal(task.Result, candidate) {
		return imageTaskCompletionCommitted
	}
	if task.Status == ImageTaskStatusProcessing {
		// The original transition may still execute after this read. Keep the
		// pending-object manifest on the task; handler-level Fail will clean it only
		// after the failed CAS wins, while a late completed CAS clears the manifest.
		return imageTaskCompletionUnknown
	}
	// A different terminal state cannot be replaced by this completion CAS, so
	// the current attempt's unique objects are definitely unreferenced.
	return imageTaskCompletionNotCommitted
}

func (s *ImageTaskService) Fail(ctx context.Context, id string, statusCode int, taskErr json.RawMessage) error {
	if !json.Valid(taskErr) {
		taskErr = imageTaskErrorJSON("api_error", "image generation failed")
	}
	_, _, err := s.finish(ctx, id, ImageTaskStatusFailed, statusCode, nil, taskErr, nil)
	if err != nil {
		return err
	}
	task, getErr := s.store.Get(ctx, id)
	if getErr != nil {
		return ErrImageTaskUnavailable.WithCause(getErr)
	}
	uploader := s.uploaderForTaskRecord(task)
	if uploader == nil {
		if len(task.PendingObjectKeys) > 0 {
			logger.L().Warn("image_task.pending_object_cleanup_deferred_binding_mismatch", zap.String("task_id", id), zap.String("storage_binding_id", task.StorageBindingID))
			return ErrImageTaskUnavailable
		}
		s.forgetTaskUploader(id)
		return nil
	}
	if task.Status != ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0 {
		s.forgetTaskUploader(id)
		return nil
	}
	cleanupErr := s.cleanupFailedPendingObjects(ctx, task, uploader)
	if cleanupErr == nil {
		s.forgetTaskUploader(id)
	}
	return errors.Join(err, cleanupErr)
}

func (s *ImageTaskService) cleanupFailedPendingObjects(ctx context.Context, task *ImageTaskRecord, uploader *ImageResultUploader) error {
	if s == nil || s.store == nil || uploader == nil || task == nil || task.Status != ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0 {
		return nil
	}
	if cleanupErr := uploader.cleanup(task.PendingObjectKeys); cleanupErr != nil {
		return cleanupErr
	}
	cleared := *task
	cleared.PendingObjectKeys = nil
	transitioned, clearErr := s.store.Transition(ctx, task.ID, ImageTaskStatusFailed, &cleared, s.ttl)
	if clearErr != nil {
		return ErrImageTaskUnavailable.WithCause(clearErr)
	}
	if transitioned {
		task.PendingObjectKeys = nil
	}
	return nil
}

func (s *ImageTaskService) scheduleFailedPendingObjectCleanup(task *ImageTaskRecord) {
	if s == nil || s.store == nil || task == nil || task.Status != ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0 {
		return
	}
	uploader := s.uploaderForTaskRecord(task)
	if uploader == nil {
		return
	}
	taskCopy := *task
	taskCopy.PendingObjectKeys = append([]string(nil), task.PendingObjectKeys...)
	s.cleanupMu.Lock()
	if s.cleanupRunning == nil {
		s.cleanupRunning = make(map[string]struct{})
	}
	if _, exists := s.cleanupRunning[task.ID]; exists {
		s.cleanupMu.Unlock()
		return
	}
	s.cleanupRunning[task.ID] = struct{}{}
	s.cleanupMu.Unlock()
	select {
	case s.cleanupSlots <- struct{}{}:
	default:
		// Keep the durable manifest for the periodic scanner or a later poll;
		// never create an unbounded goroutine per GET under cleanup pressure.
		s.cleanupMu.Lock()
		delete(s.cleanupRunning, taskCopy.ID)
		s.cleanupMu.Unlock()
		return
	}

	// Register the goroutine while holding cleanupMu. Close marks the service
	// closed under the same lock before waiting, so WaitGroup.Add can never
	// race with a zero-counter Wait after shutdown has begun.
	s.cleanupMu.Lock()
	if s.cleanupClosed {
		delete(s.cleanupRunning, taskCopy.ID)
		s.cleanupMu.Unlock()
		<-s.cleanupSlots
		return
	}
	s.cleanupWG.Add(1)
	s.cleanupMu.Unlock()
	go func() {
		defer s.cleanupWG.Done()
		defer func() { <-s.cleanupSlots }()
		defer func() {
			s.cleanupMu.Lock()
			delete(s.cleanupRunning, taskCopy.ID)
			s.cleanupMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(s.cleanupCtx, imageStorageCleanupTimeout+imageTaskReconcileTimeout)
		defer cancel()
		if cleanupErr := s.cleanupFailedPendingObjects(ctx, &taskCopy, uploader); cleanupErr != nil {
			// The durable manifest remains on the failed task, so a later poll can
			// schedule another bounded attempt without delaying this GET.
			logger.L().Warn("image_task.pending_object_cleanup_retry_failed", zap.String("task_id", taskCopy.ID), zap.Error(cleanupErr))
		} else {
			s.forgetTaskUploader(taskCopy.ID)
		}
	}()
}

func (s *ImageTaskService) trackPendingObjects(ctx context.Context, id string, keys []string) (bool, error) {
	if len(keys) == 0 {
		return true, nil
	}
	if s == nil || s.store == nil {
		return false, ErrImageTaskUnavailable
	}
	task, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return false, ErrImageTaskNotFound
		}
		return false, ErrImageTaskUnavailable.WithCause(err)
	}
	if task.Status != ImageTaskStatusProcessing {
		return false, nil
	}
	task.PendingObjectKeys = append([]string(nil), keys...)
	tracked, err := s.store.Transition(ctx, id, ImageTaskStatusProcessing, task, s.ttl)
	if err != nil {
		return false, ErrImageTaskUnavailable.WithCause(err)
	}
	return tracked, nil
}

func (s *ImageTaskService) finish(ctx context.Context, id, status string, statusCode int, result, taskErr json.RawMessage, resultObjectKeys []string) (transitioned, transitionAttempted bool, err error) {
	if s == nil || s.store == nil {
		return false, false, ErrImageTaskUnavailable
	}
	task, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrImageTaskNotFound) {
			return false, false, ErrImageTaskNotFound
		}
		return false, false, ErrImageTaskUnavailable.WithCause(err)
	}
	if task.Status != ImageTaskStatusProcessing {
		// 终态不可逆：超时后的失败补写不能覆盖已经成功提交的 completed，
		// 迟到的 completion 也不能覆盖先落库的 failed。
		return false, false, nil
	}
	now := time.Now().UTC()
	completedAt := now.Unix()
	task.Status = status
	task.HTTPStatus = statusCode
	task.Result = result
	task.Error = taskErr
	if status == ImageTaskStatusCompleted {
		task.ResultObjectKeys = append([]string(nil), resultObjectKeys...)
		task.PendingObjectKeys = nil
	}
	task.CompletedAt = &completedAt
	task.ExpiresAt = now.Add(s.ttl).Unix()
	transitioned, err = s.store.Transition(ctx, id, ImageTaskStatusProcessing, task, s.ttl)
	if err != nil {
		return false, true, ErrImageTaskUnavailable.WithCause(err)
	}
	if !transitioned {
		return false, true, nil
	}
	return true, true, nil
}

func imageTaskToPublic(task *ImageTaskRecord) *ImageTask {
	if task == nil {
		return nil
	}
	return &ImageTask{
		ID:          task.ID,
		TaskID:      task.ID,
		Object:      "image.generation.task",
		Status:      task.Status,
		HTTPStatus:  task.HTTPStatus,
		ImageURL:    firstImageTaskURL(task.Result),
		Result:      task.Result,
		Error:       task.Error,
		CreatedAt:   task.CreatedAt,
		CompletedAt: task.CompletedAt,
		ExpiresAt:   task.ExpiresAt,
	}
}

func firstImageTaskURL(result json.RawMessage) string {
	if len(result) == 0 || !json.Valid(result) {
		return ""
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil || len(response.Data) == 0 {
		return ""
	}
	return strings.TrimSpace(response.Data[0].URL)
}

func imageTaskResultURLs(result json.RawMessage) []string {
	if len(result) == 0 || !json.Valid(result) {
		return nil
	}
	var response struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(result, &response) != nil {
		return nil
	}
	urls := make([]string, 0, len(response.Data))
	for _, item := range response.Data {
		if value := safeImageTaskResultURL(item.URL); value != "" {
			urls = append(urls, value)
		}
	}
	return urls
}

func safeImageTaskResultURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || !strings.EqualFold(parsed.Scheme, "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return ""
	}
	return value
}

// ImageTaskResultURLs returns the already-public URLs from a legacy task
// payload. New durable tasks should use ImageTaskService.ResolveResultURLs so
// access grants are re-issued by the object store.
func ImageTaskResultURLs(result json.RawMessage) []string {
	return imageTaskResultURLs(result)
}

func imageTaskErrorJSON(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"type": errorType, "message": message})
	return data
}
