package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type asyncImageMemoryStore struct {
	mu    sync.RWMutex
	tasks map[string]*service.ImageTaskRecord
}

type timeoutAwareImageStore struct {
	mu                 sync.Mutex
	task               *service.ImageTaskRecord
	completionDeadline bool
	failureDeadline    bool
	completionRelease  chan struct{}
	commitBeforeReturn bool
}

type handlerImageStorage struct {
	saved   []string
	deleted []string
}

func (s *handlerImageStorage) Save(_ context.Context, key, _ string, _ []byte) (string, error) {
	s.saved = append(s.saved, key)
	return "https://cdn.test/" + key, nil
}

func (s *handlerImageStorage) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *timeoutAwareImageStore) Get(_ context.Context, _ string) (*service.ImageTaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *s.task
	return &copy, nil
}

func (s *timeoutAwareImageStore) Save(_ context.Context, task *service.ImageTaskRecord, _ time.Duration) error {
	s.mu.Lock()
	copy := *task
	s.task = &copy
	s.mu.Unlock()
	return nil
}

func (s *timeoutAwareImageStore) ListPending(_ context.Context, _ int) ([]*service.ImageTaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || (s.task.Status != service.ImageTaskStatusProcessing &&
		(s.task.Status != service.ImageTaskStatusFailed || len(s.task.PendingObjectKeys) == 0)) {
		return nil, nil
	}
	copy := *s.task
	copy.PendingObjectKeys = append([]string(nil), s.task.PendingObjectKeys...)
	return []*service.ImageTaskRecord{&copy}, nil
}

func (s *timeoutAwareImageStore) Transition(ctx context.Context, _ string, expectedStatus string, task *service.ImageTaskRecord, _ time.Duration) (bool, error) {
	if task.Status == service.ImageTaskStatusCompleted {
		s.mu.Lock()
		_, s.completionDeadline = ctx.Deadline()
		if s.commitBeforeReturn && s.task.Status == expectedStatus {
			copy := *task
			s.task = &copy
		}
		s.mu.Unlock()
		<-s.completionRelease // Deliberately ignores ctx to exercise the handler watchdog.
		if s.commitBeforeReturn {
			return true, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if task.Status == service.ImageTaskStatusFailed {
		_, s.failureDeadline = ctx.Deadline()
	}
	if s.task.Status != expectedStatus {
		return false, nil
	}
	copy := *task
	s.task = &copy
	return true, nil
}

func (s *asyncImageMemoryStore) Save(_ context.Context, task *service.ImageTaskRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *task
	copy.Result = append(json.RawMessage(nil), task.Result...)
	copy.Error = append(json.RawMessage(nil), task.Error...)
	s.tasks[task.ID] = &copy
	return nil
}

func (s *asyncImageMemoryStore) Get(_ context.Context, id string) (*service.ImageTaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task := s.tasks[id]
	if task == nil {
		return nil, service.ErrImageTaskNotFound
	}
	copy := *task
	copy.Result = append(json.RawMessage(nil), task.Result...)
	copy.Error = append(json.RawMessage(nil), task.Error...)
	return &copy, nil
}

func (s *asyncImageMemoryStore) Transition(_ context.Context, id, expectedStatus string, task *service.ImageTaskRecord, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.tasks[id]
	if current == nil {
		return false, service.ErrImageTaskNotFound
	}
	if current.Status != expectedStatus {
		return false, nil
	}
	copy := *task
	copy.Result = append(json.RawMessage(nil), task.Result...)
	copy.Error = append(json.RawMessage(nil), task.Error...)
	s.tasks[id] = &copy
	return true, nil
}

func (s *asyncImageMemoryStore) ListPending(_ context.Context, _ int) ([]*service.ImageTaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*service.ImageTaskRecord, 0)
	for _, task := range s.tasks {
		if task == nil || (task.Status != service.ImageTaskStatusProcessing &&
			(task.Status != service.ImageTaskStatusFailed || len(task.PendingObjectKeys) == 0)) {
			continue
		}
		copy := *task
		copy.PendingObjectKeys = append([]string(nil), task.PendingObjectKeys...)
		result = append(result, &copy)
	}
	return result, nil
}

func TestAsyncImageHandlerSubmitAndPoll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
	release := make(chan struct{})
	h := &AsyncImageHandler{tasks: tasks, admission: newAsyncImageAdmission(1, 1)}
	h.execute = func(_ string, c *gin.Context) {
		<-release
		c.JSON(http.StatusOK, gin.H{"created": 123, "data": []gin.H{{"url": "https://example.test/image.png"}}})
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		groupID := int64(3)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
			ID:      9,
			UserID:  7,
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true},
		})
		c.Next()
	})
	router.POST("/v1/images/generations/async", h.Submit)
	router.GET("/v1/images/tasks/:task_id", h.Get)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-1","prompt":"cat"}`)).WithContext(requestCtx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Equal(t, "3", w.Header().Get("Retry-After"))

	var accepted struct {
		TaskID  string `json:"task_id"`
		Status  string `json:"status"`
		PollURL string `json:"poll_url"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &accepted))
	require.Equal(t, service.ImageTaskStatusProcessing, accepted.Status)
	require.Equal(t, "/v1/images/tasks/"+accepted.TaskID, accepted.PollURL)
	require.Equal(t, accepted.PollURL, w.Header().Get("Location"))

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-1","prompt":"dog"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondWriter := httptest.NewRecorder()
	router.ServeHTTP(secondWriter, secondReq)
	require.Equal(t, http.StatusTooManyRequests, secondWriter.Code)
	require.Contains(t, secondWriter.Body.String(), "rate_limit_error")

	// The detached background request must survive completion of/cancellation
	// from the short submission request.
	cancelRequest()
	close(release)
	require.Eventually(t, func() bool {
		got, err := tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, accepted.TaskID)
		return err == nil && got.Status == service.ImageTaskStatusCompleted
	}, time.Second, 10*time.Millisecond)
	releaseAfterCompletion, admittedAfterCompletion := h.admission.acquire(9)
	require.True(t, admittedAfterCompletion, "completed task must release its admission slot")
	releaseAfterCompletion()

	pollReq := httptest.NewRequest(http.MethodGet, accepted.PollURL, nil)
	pollWriter := httptest.NewRecorder()
	router.ServeHTTP(pollWriter, pollReq)
	require.Equal(t, http.StatusOK, pollWriter.Code)
	require.Equal(t, "no-store", pollWriter.Header().Get("Cache-Control"))
	require.Empty(t, pollWriter.Header().Get("Retry-After"))
	require.Contains(t, pollWriter.Body.String(), "https://example.test/image.png")
}

func TestAsyncImageAdmissionBoundsGlobalAndPerAPIKeyInflightTasks(t *testing.T) {
	admission := newAsyncImageAdmission(2, 1)
	releaseOne, ok := admission.acquire(101)
	require.True(t, ok)
	_, ok = admission.acquire(101)
	require.False(t, ok, "one API key must not exceed its configured in-flight limit")

	releaseTwo, ok := admission.acquire(202)
	require.True(t, ok)
	_, ok = admission.acquire(303)
	require.False(t, ok, "global in-flight capacity must be bounded")

	releaseOne()
	releaseOne()
	releaseThree, ok := admission.acquire(303)
	require.True(t, ok, "released capacity must be reusable")
	releaseTwo()
	releaseThree()
}

// When object storage is not configured the feature is fully disabled: the
// endpoints must return 404 without creating a task or writing to Redis.
func TestAsyncImageHandlerDisabledReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &asyncImageMemoryStore{tasks: make(map[string]*service.ImageTaskRecord)}
	tasks := service.NewImageTaskServiceWithOptions(store, time.Hour, time.Minute) // enabled == false
	h := &AsyncImageHandler{tasks: tasks}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		groupID := int64(3)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
			ID:      9,
			UserID:  7,
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true},
		})
		c.Next()
	})
	router.POST("/v1/images/generations/async", h.Submit)
	router.GET("/v1/images/tasks/:task_id", h.Get)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"gpt-image-1","prompt":"cat"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "not enabled")

	pollReq := httptest.NewRequest(http.MethodGet, "/v1/images/tasks/imgtask_missing", nil)
	pollWriter := httptest.NewRecorder()
	router.ServeHTTP(pollWriter, pollReq)
	require.Equal(t, http.StatusNotFound, pollWriter.Code)

	// No task was created / persisted.
	require.Empty(t, store.tasks)
}

func TestAsyncImageHandlerBoundsCompletionAndFailureStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &timeoutAwareImageStore{task: &service.ImageTaskRecord{
		ID:        "imgtask_timeout",
		UserID:    7,
		APIKeyID:  9,
		Status:    service.ImageTaskStatusProcessing,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, completionRelease: make(chan struct{})}
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
	h := &AsyncImageHandler{
		tasks:             tasks,
		completionTimeout: 20 * time.Millisecond,
		failureTimeout:    time.Second,
	}
	h.execute = func(_ string, c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
	}
	recorder := httptest.NewRecorder()
	taskCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	h.run("imgtask_timeout", service.PlatformOpenAI, taskCtx, recorder, func() {})
	close(store.completionRelease)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.True(t, store.completionDeadline)
	require.True(t, store.failureDeadline)
	require.Equal(t, service.ImageTaskStatusFailed, store.task.Status)
	require.Contains(t, string(store.task.Error), "failed to finalize generated image")
}

func TestAsyncImageHandlerDoesNotOverwriteCommittedCompletionAfterTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &timeoutAwareImageStore{task: &service.ImageTaskRecord{
		ID:        "imgtask_committed",
		UserID:    7,
		APIKeyID:  9,
		Status:    service.ImageTaskStatusProcessing,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, completionRelease: make(chan struct{}), commitBeforeReturn: true}
	tasks := service.NewImageTaskServiceWithUploader(store, nil, time.Hour, time.Minute)
	h := &AsyncImageHandler{tasks: tasks, completionTimeout: 20 * time.Millisecond, failureTimeout: time.Second}
	h.execute = func(_ string, c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"url": "https://example.test/image.png"}}})
	}
	recorder := httptest.NewRecorder()
	taskCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	h.run("imgtask_committed", service.PlatformOpenAI, taskCtx, recorder, func() {})
	close(store.completionRelease)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, service.ImageTaskStatusCompleted, store.task.Status)
	require.Empty(t, store.task.Error)
}

func TestAsyncImageHandlerCleansPendingObjectWhenFailureWinsAfterCompletionTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &timeoutAwareImageStore{task: &service.ImageTaskRecord{
		ID: "imgtask_cleanup", UserID: 7, APIKeyID: 9,
		Status: service.ImageTaskStatusProcessing, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}, completionRelease: make(chan struct{})}
	defer close(store.completionRelease)
	storage := &handlerImageStorage{}
	uploader := service.NewImageResultUploader(storage, "images/", 0, nil)
	tasks := service.NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	h := &AsyncImageHandler{tasks: tasks, completionTimeout: 20 * time.Millisecond, failureTimeout: time.Second}
	b64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nhandler-test"))
	h.execute = func(_ string, c *gin.Context) {
		c.Data(http.StatusOK, "application/json", []byte(`{"data":[{"b64_json":"`+b64+`"}]}`))
	}
	recorder := httptest.NewRecorder()
	taskCtx, _ := gin.CreateTestContext(recorder)
	taskCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	h.run("imgtask_cleanup", service.PlatformOpenAI, taskCtx, recorder, func() {})

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, service.ImageTaskStatusFailed, store.task.Status)
	require.Len(t, storage.saved, 1)
	require.Equal(t, storage.saved, storage.deleted)
	require.Empty(t, store.task.PendingObjectKeys)
}
