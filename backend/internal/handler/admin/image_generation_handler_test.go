package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageStorageBrowserStub struct {
	prefix string
}

func (s *imageStorageBrowserStub) List(_ context.Context, prefix, _ string, _ int) (*service.ImageStorageObjectPage, error) {
	s.prefix = prefix
	return &service.ImageStorageObjectPage{Items: []service.ImageStorageObject{{Key: prefix + "imgtask_1-0.png"}}}, nil
}

func TestImageGenerationListUsesConfiguredAsyncPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewImageStorageSettingService(nil, nil, nil, nil, config.ImageStorageConfig{
		Enabled: true, Bucket: "images", Prefix: "images/", AccessKeyID: "key", SecretAccessKey: "secret",
	})
	browser := &imageStorageBrowserStub{}
	h := NewImageGenerationHandler(settings, func(context.Context, *config.ImageStorageConfig) (service.ImageStorageBrowser, error) {
		return browser, nil
	})
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
	})
	router := gin.New()
	router.GET("/admin/image-generations", h.List)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/image-generations?prefix=backups/", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, factoryCalled)
}
