package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
)

type imageTaskCompletionReconcileResult int

const (
	imageTaskCompletionUnknown imageTaskCompletionReconcileResult = iota
	imageTaskCompletionCommitted
	imageTaskCompletionNotCommitted
)

// ImageTaskRecord is the private Redis representation of an asynchronous image
// request. Ownership fields are intentionally omitted from the public view.
type ImageTaskRecord struct {
	ID                string          `json:"id"`
	UserID            int64           `json:"user_id"`
	APIKeyID          int64           `json:"api_key_id"`
	Status            string          `json:"status"`
	HTTPStatus        int             `json:"http_status,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
	PendingObjectKeys []string        `json:"pending_object_keys,omitempty"`
	CreatedAt         int64           `json:"created_at"`
	CompletedAt       *int64          `json:"completed_at,omitempty"`
	ExpiresAt         int64           `json:"expires_at"`
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
	cleanupRunning   map[string]struct{}
	cleanupSlots     chan struct{}
	cleanupStop      chan struct{}
	cleanupStopOnce  sync.Once
	cleanupCancel    context.CancelFunc
	cleanupCtx       context.Context
	cleanupWG        sync.WaitGroup
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

// Close stops background cleanup. It is safe to call more than once.
func (s *ImageTaskService) Close() {
	if s == nil {
		return
	}
	s.cleanupStopOnce.Do(func() { close(s.cleanupStop) })
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
	uploader, _ := s.current()
	if s.store == nil || uploader == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imageStorageCleanupTimeout+imageTaskReconcileTimeout)
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
			if err := s.cleanupFailedPendingObjects(ctx, task, uploader); err != nil {
				logger.L().Warn("image_task.pending_object_cleanup_failed", zap.String("task_id", task.ID), zap.Error(err))
			}
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

func (s *ImageTaskService) Create(ctx context.Context, owner ImageTaskOwner) (*ImageTask, error) {
	if s == nil || s.store == nil {
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
	if err := s.store.Save(ctx, task, s.ttl); err != nil {
		return nil, ErrImageTaskUnavailable.WithCause(err)
	}
	return imageTaskToPublic(task), nil
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
	uploader, _ := s.current()
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
			return nil
		}
		result = rewritten.payload
		uploadedKeys = rewritten.keys
	}
	transitioned, transitionAttempted, err := s.finish(ctx, id, ImageTaskStatusCompleted, statusCode, result, nil)
	cleanupUploaded := err == nil && !transitioned
	if err != nil && transitionAttempted {
		switch s.reconcileCompletionAfterTransitionError(id, result) {
		case imageTaskCompletionCommitted:
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
		}
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
	_, _, err := s.finish(ctx, id, ImageTaskStatusFailed, statusCode, nil, taskErr)
	uploader, _ := s.current()
	if err != nil || uploader == nil {
		return err
	}
	task, getErr := s.store.Get(ctx, id)
	if getErr != nil {
		return errors.Join(err, ErrImageTaskUnavailable.WithCause(getErr))
	}
	if task.Status != ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0 {
		return nil
	}
	return errors.Join(err, s.cleanupFailedPendingObjects(ctx, task, uploader))
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
	uploader, _ := s.current()
	if s == nil || s.store == nil || uploader == nil || task == nil || task.Status != ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0 {
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

	s.cleanupWG.Add(1)
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

func (s *ImageTaskService) finish(ctx context.Context, id, status string, statusCode int, result, taskErr json.RawMessage) (transitioned, transitionAttempted bool, err error) {
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

func imageTaskErrorJSON(errorType, message string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"type": errorType, "message": message})
	return data
}
