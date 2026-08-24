package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageStorageBrowserStub struct {
	prefix       string
	resolvedKeys []string
}

func (s *imageStorageBrowserStub) List(_ context.Context, prefix, _ string, _ int) (*service.ImageStorageObjectPage, error) {
	s.prefix = prefix
	return &service.ImageStorageObjectPage{
		Items:      []service.ImageStorageObject{{Key: prefix + "imgtask_1-0.png"}},
		TotalCount: 1,
	}, nil
}

func (s *imageStorageBrowserStub) ResolveURLs(_ context.Context, keys []string) (map[string]string, error) {
	s.resolvedKeys = append([]string(nil), keys...)
	resolved := make(map[string]string, len(keys))
	for _, key := range keys {
		resolved[key] = "https://fresh.example.test/" + key
	}
	return resolved, nil
}

type imageTaskAdminStoreStub struct {
	page  *service.ImageTaskAdminPage
	query service.ImageTaskAdminQuery
}

func (s *imageTaskAdminStoreStub) Save(context.Context, *service.ImageTaskRecord, time.Duration) error {
	return nil
}

func (s *imageTaskAdminStoreStub) Get(context.Context, string) (*service.ImageTaskRecord, error) {
	return nil, service.ErrImageTaskNotFound
}

func (s *imageTaskAdminStoreStub) Transition(context.Context, string, string, *service.ImageTaskRecord, time.Duration) (bool, error) {
	return false, nil
}

func (s *imageTaskAdminStoreStub) ListPending(context.Context, int) ([]*service.ImageTaskRecord, error) {
	return nil, nil
}

func (s *imageTaskAdminStoreStub) ListAdmin(_ context.Context, query service.ImageTaskAdminQuery) (*service.ImageTaskAdminPage, error) {
	s.query = query
	return s.page, nil
}

func TestImageGenerationListTasksParsesDateRangeInUserTimezone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &imageTaskAdminStoreStub{page: &service.ImageTaskAdminPage{}}
	tasks := service.NewImageTaskService(store)
	t.Cleanup(tasks.Close)
	h := NewImageGenerationHandler(nil, nil, tasks)
	router := gin.New()
	router.GET("/admin/image-generations/tasks", h.ListTasks)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations/tasks?start_date=2026-08-01&end_date=2026-08-02&timezone=Asia%2FShanghai", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	start, err := time.ParseInLocation("2006-01-02", "2026-08-01", time.FixedZone("CST", 8*60*60))
	require.NoError(t, err)
	require.Equal(t, start.Unix(), store.query.StartAt)
	require.Equal(t, start.AddDate(0, 0, 2).Unix(), store.query.EndAt)
}

func TestImageGenerationListUsesConfiguredAsyncPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewImageStorageSettingService(nil, nil, nil, nil, config.ImageStorageConfig{
		Enabled: true, Bucket: "images", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	})
	browser := &imageStorageBrowserStub{}
	h := NewImageGenerationHandler(settings, func(context.Context, *config.ImageStorageConfig) (service.ImageStorageBrowser, error) {
		return browser, nil
	}, nil)
	router := gin.New()
	router.GET("/admin/image-generations", h.List)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "images/", browser.prefix)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "images", data["bucket"])
	require.Equal(t, float64(1), data["total_count"])
}

func TestImageGenerationListRejectsPrefixOutsideAsyncNamespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewImageStorageSettingService(nil, nil, nil, nil, config.ImageStorageConfig{
		Enabled: true, Bucket: "shared", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	})
	factoryCalled := false
	h := NewImageGenerationHandler(settings, func(context.Context, *config.ImageStorageConfig) (service.ImageStorageBrowser, error) {
		factoryCalled = true
		return &imageStorageBrowserStub{}, nil
	}, nil)
	router := gin.New()
	router.GET("/admin/image-generations", h.List)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations?prefix=backups/", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, factoryCalled)
}

func TestImageGenerationListTasksFiltersSensitiveTaskFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().Unix()
	store := &imageTaskAdminStoreStub{page: &service.ImageTaskAdminPage{Tasks: []*service.ImageTaskRecord{{
		ID: "imgtask_admin_1", UserID: 7, APIKeyID: 9, Platform: service.PlatformOpenAI,
		Operation: "generation", Model: "gpt-image-2", ImageCount: 1,
		Status: service.ImageTaskStatusCompleted, HTTPStatus: http.StatusOK,
		Result:            json.RawMessage(`{"prompt":"private prompt","data":[{"url":"https://cdn.example.test/image.png"}]}`),
		PendingObjectKeys: []string{"images/internal-key.png"}, CreatedAt: now - 2, ExpiresAt: now + 3600,
	}}}}
	tasks := service.NewImageTaskService(store)
	t.Cleanup(tasks.Close)

	h := NewImageGenerationHandler(nil, nil, tasks)
	router := gin.New()
	router.GET("/admin/image-generations/tasks", h.ListTasks)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations/tasks", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.NotContains(t, body, "private prompt")
	require.NotContains(t, body, "internal-key.png")
	var envelope struct {
		Data struct {
			Items []struct {
				ResultCount int      `json:"result_count"`
				ResultURLs  []string `json:"result_urls"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 1)
	require.Equal(t, 1, envelope.Data.Items[0].ResultCount)
	require.Equal(t, []string{"https://cdn.example.test/image.png"}, envelope.Data.Items[0].ResultURLs)
}

func TestImageGenerationListTasksResolvesPersistedObjectKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storageCfg := config.ImageStorageConfig{
		Enabled: true, Bucket: "images", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	settings := service.NewImageStorageSettingService(nil, nil, nil, nil, storageCfg)
	browser := &imageStorageBrowserStub{}
	now := time.Now().Unix()
	store := &imageTaskAdminStoreStub{page: &service.ImageTaskAdminPage{Tasks: []*service.ImageTaskRecord{{
		ID: "imgtask_history_1", Status: service.ImageTaskStatusCompleted, HTTPStatus: http.StatusOK,
		CreatedAt: now - 10, ExpiresAt: now + 3600,
		Result:           json.RawMessage(`{"data":[{"url":"https://expired.example.test/old.png"}]}`),
		ResultObjectKeys: []string{"images/history-1.png"},
		StorageBindingID: service.ImageStorageBindingID(&storageCfg),
	}}}}
	tasks := service.NewImageTaskService(store)
	t.Cleanup(tasks.Close)
	h := NewImageGenerationHandler(settings, func(context.Context, *config.ImageStorageConfig) (service.ImageStorageBrowser, error) {
		return browser, nil
	}, tasks)
	router := gin.New()
	router.GET("/admin/image-generations/tasks", h.ListTasks)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations/tasks", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{"images/history-1.png"}, browser.resolvedKeys)
	var envelope struct {
		Data struct {
			Items []struct {
				ResultCount int      `json:"result_count"`
				ResultURLs  []string `json:"result_urls"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 1)
	require.Equal(t, 1, envelope.Data.Items[0].ResultCount)
	require.Equal(t, []string{"https://fresh.example.test/images/history-1.png"}, envelope.Data.Items[0].ResultURLs)
}

func TestImageGenerationListTasksDegradesWhenObjectStorageIsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	storageCfg := config.ImageStorageConfig{
		Enabled: true, Bucket: "images", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	}
	settings := service.NewImageStorageSettingService(nil, nil, nil, nil, storageCfg)
	now := time.Now().Unix()
	store := &imageTaskAdminStoreStub{page: &service.ImageTaskAdminPage{Tasks: []*service.ImageTaskRecord{{
		ID: "imgtask_history_storage_down", Status: service.ImageTaskStatusCompleted, HTTPStatus: http.StatusOK,
		CreatedAt: now - 10, ExpiresAt: now + 3600,
		Result:           json.RawMessage(`{"data":[{"url":"https://expired.example.test/old.png"}]}`),
		ResultObjectKeys: []string{"images/history-storage-down.png"},
		StorageBindingID: service.ImageStorageBindingID(&storageCfg),
	}}}}
	tasks := service.NewImageTaskService(store)
	t.Cleanup(tasks.Close)
	h := NewImageGenerationHandler(settings, func(context.Context, *config.ImageStorageConfig) (service.ImageStorageBrowser, error) {
		return nil, fmt.Errorf("storage unavailable")
	}, tasks)
	router := gin.New()
	router.GET("/admin/image-generations/tasks", h.ListTasks)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations/tasks", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data struct {
			Items []struct {
				ResultCount int      `json:"result_count"`
				ResultURLs  []string `json:"result_urls"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Items, 1)
	require.Equal(t, 1, envelope.Data.Items[0].ResultCount)
	require.Empty(t, envelope.Data.Items[0].ResultURLs)
}
