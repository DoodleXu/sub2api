//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWebConsoleAuthorizeEndpointUsesConfiguredAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerPublicRepoStub{
		values: map[string]string{
			service.SettingKeyAPIBaseURL:      "https://api.example.com",
			service.SettingKeyCustomEndpoints: `[{ "name": "Alt", "endpoint": "https://alt.example.com/api", "description": "" }]`,
		},
	}
	h := &WebConsoleImageTaskHandler{
		settingService: service.NewSettingService(repo, &config.Config{}),
	}
	c := webConsoleTestContext()

	resolved, err := h.authorizeWebConsoleEndpoint(c, "https://api.example.com")
	require.NoError(t, err)
	require.Equal(t, "https://api.example.com/v1", resolved)

	resolved, err = h.authorizeWebConsoleEndpoint(c, "https://alt.example.com/api")
	require.NoError(t, err)
	require.Equal(t, "https://alt.example.com/api/v1", resolved)

	_, err = h.authorizeWebConsoleEndpoint(c, "https://evil.example.com")
	require.Error(t, err)
}

func TestWebConsoleAuthorizeEndpointRejectsRelativeAndCredentialURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WebConsoleImageTaskHandler{
		settingService: service.NewSettingService(&settingHandlerPublicRepoStub{
			values: map[string]string{
				service.SettingKeyAPIBaseURL: "https://api.example.com",
			},
		}, &config.Config{}),
	}
	c := webConsoleTestContext()

	_, err := h.authorizeWebConsoleEndpoint(c, "/v1")
	require.ErrorContains(t, err, "absolute")

	_, err = h.authorizeWebConsoleEndpoint(c, "https://user:pass@api.example.com")
	require.ErrorContains(t, err, "credentials")
}

func TestWebConsoleUserAssetResponsesUseSignedPublicAssetURL(t *testing.T) {
	imageService := service.NewImageGenerationArchiveService(nil, nil, nil, &config.Config{})
	h := &WebConsoleImageTaskHandler{imageService: imageService}

	assets := h.userAssetResponses(42, []*service.ImageGenerationAsset{{ID: 7, RecordID: 42, AssetIndex: 0, MimeType: "image/png", Extension: ".png"}})

	require.Len(t, assets, 1)
	rawURL, ok := assets[0]["url"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/image-assets/7", parsed.Path)
	require.Equal(t, service.ImageAssetScopeWebConsole, parsed.Query().Get("scope"))
	require.NotEmpty(t, parsed.Query().Get("sig"))
	require.True(t, imageService.VerifyAssetToken(7, parsed.Query().Get("scope"), parsed.Query().Get("expires"), parsed.Query().Get("sig"), time.Now().UTC()))
}

func TestWebConsoleHTTPClientOptionsDoNotCutOffSlowImageHeaders(t *testing.T) {
	opts := webConsoleHTTPClientOptions()

	require.Equal(t, 10*time.Minute, opts.Timeout)
	require.Zero(t, opts.ResponseHeaderTimeout)
	require.True(t, opts.ValidateResolvedIP)
	require.Equal(t, 4, opts.MaxIdleConnsPerHost)
	require.Equal(t, 8, opts.MaxConnsPerHost)
}

func webConsoleTestContext() *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/web-console/image-tasks", nil)
	return c
}
