//go:build unit

package handler

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
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

	assets := h.userAssetResponses(42, []*service.ImageGenerationAsset{{ID: 7, RecordID: 42, AssetIndex: 0, MimeType: "image/png", Extension: ".png", SHA256: "abc123"}})

	require.Len(t, assets, 1)
	rawURL, ok := assets[0]["url"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/image-assets/7", parsed.Path)
	require.Equal(t, service.ImageAssetScopeWebConsole, parsed.Query().Get("scope"))
	require.Equal(t, "abc123", parsed.Query().Get("v"))
	require.NotEmpty(t, parsed.Query().Get("expires"))
	require.NotEmpty(t, parsed.Query().Get("sig"))
	require.True(t, imageService.VerifyStableAssetToken(7, parsed.Query().Get("scope"), parsed.Query().Get("v"), parsed.Query().Get("expires"), parsed.Query().Get("sig"), time.Now().UTC()))
}

func TestGetSignedAssetRejectsBadStableTokenBeforeAssetLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &webConsoleImageTaskRepoStub{}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "asset_id", Value: "7"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/image-assets/7?v=abc123&expires=1800000000&scope=web-console-image-task&sig=bad", nil)

	h.GetSignedAsset(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 0, repo.getAssetCalls)
}

func TestGetSignedAssetRejectsExpiredStableTokenBeforeAssetLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imageService := service.NewImageGenerationArchiveService(nil, nil, nil, &config.Config{})
	signedURL := imageService.SignStableAssetURLPath("/api/v1/image-assets/7", 7, service.ImageAssetScopeWebConsole, "abc123")
	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("expires", "1")
	parsed.RawQuery = query.Encode()
	repo := &webConsoleImageTaskRepoStub{}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "asset_id", Value: "7"}}
	c.Request = httptest.NewRequest(http.MethodGet, parsed.String(), nil)

	h.GetSignedAsset(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 0, repo.getAssetCalls)
}

func TestGetSignedAssetVerifiesStableTokenVersionAfterAssetLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imageService := service.NewImageGenerationArchiveService(nil, nil, nil, &config.Config{})
	signedURL := imageService.SignStableAssetURLPath("/api/v1/image-assets/7", 7, service.ImageAssetScopeWebConsole, "oldhash")
	parsed, err := url.Parse(signedURL)
	require.NoError(t, err)
	repo := &webConsoleImageTaskRepoStub{
		asset: &service.ImageGenerationAsset{
			ID:         7,
			RecordID:   42,
			MimeType:   "image/png",
			Extension:  ".png",
			SHA256:     "newhash",
			StorageKey: "images/7.png",
		},
	}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "asset_id", Value: "7"}}
	c.Request = httptest.NewRequest(http.MethodGet, parsed.String(), nil)

	h.GetSignedAsset(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, 1, repo.getAssetCalls)
}

func TestWebConsoleHTTPClientOptionsDoNotCutOffSlowImageHeaders(t *testing.T) {
	opts := webConsoleHTTPClientOptions()

	require.Equal(t, 10*time.Minute, opts.Timeout)
	require.Zero(t, opts.ResponseHeaderTimeout)
	require.True(t, opts.ValidateResolvedIP)
	require.Equal(t, 4, opts.MaxIdleConnsPerHost)
	require.Equal(t, 8, opts.MaxConnsPerHost)
}

func TestApplyWebConsoleCodexRequestHeadersReusesGatewayCodexPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/responses", nil)

	applyWebConsoleCodexRequestHeaders(req)

	require.Equal(t, webConsoleCodexUserAgent, req.Header.Get("User-Agent"))
	require.Equal(t, webConsoleCodexOriginator, req.Header.Get("originator"))
	require.Equal(t, webConsoleCodexVersion, req.Header.Get("version"))
	require.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
	require.Equal(t, "application/json", req.Header.Get("Accept"))
}

func TestWebConsoleUpstreamErrorDetailPrefersStructuredMessage(t *testing.T) {
	detail := webConsoleUpstreamErrorDetail([]byte(`{"error":{"type":"server_error","code":"bad_gateway","message":"upstream timed out"}}`))

	require.Equal(t, "bad_gateway: upstream timed out", detail)
}

func TestRunWebConsoleImageRequestsRunsMultipleImagesConcurrently(t *testing.T) {
	var inFlight int32
	var maxInFlight int32
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			previous := atomic.LoadInt32(&maxInFlight)
			if current <= previous || atomic.CompareAndSwapInt32(&maxInFlight, previous, current) {
				break
			}
		}
		defer atomic.AddInt32(&inFlight, -1)
		index := atomic.AddInt32(&requestCount, 1)
		time.Sleep(120 * time.Millisecond)
		_, _ = fmt.Fprintf(w, `{"output":[{"type":"image_generation_call","b64_json":%q}]}`, webConsoleTestBase64Image(index))
	}))
	defer server.Close()
	withWebConsoleHTTPClient(t, server.Client())

	started := time.Now()
	images, err := runWebConsoleImageRequests(context.Background(), createWebConsoleImageTaskRequest{
		Model:    "gpt-5.5",
		Prompt:   "draw",
		Endpoint: server.URL + "/v1",
		Options:  []byte(`{"count":4,"outputFormat":"png"}`),
	}, "sk-test")

	require.NoError(t, err)
	require.Len(t, images, 4)
	require.Equal(t, int32(4), atomic.LoadInt32(&requestCount))
	require.GreaterOrEqual(t, atomic.LoadInt32(&maxInFlight), int32(2))
	require.Less(t, time.Since(started), 400*time.Millisecond)
}

func TestRunWebConsoleImageRequestsFailsFastOnUpstreamError(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := atomic.AddInt32(&requestCount, 1)
		if index == 1 {
			http.Error(w, `{"error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`, http.StatusBadGateway)
			return
		}
		time.Sleep(time.Second)
		_, _ = w.Write([]byte(`{"output":[{"type":"image_generation_call","b64_json":"` + webConsoleTestBase64Image(index) + `"}]}`))
	}))
	defer server.Close()
	withWebConsoleHTTPClient(t, &http.Client{Transport: server.Client().Transport, Timeout: 5 * time.Second})

	_, err := runWebConsoleImageRequests(context.Background(), createWebConsoleImageTaskRequest{
		Model:    "gpt-5.5",
		Prompt:   "draw",
		Endpoint: server.URL + "/v1",
		Options:  []byte(`{"count":4}`),
	}, "sk-test")

	require.ErrorContains(t, err, "image task upstream failed: status 502")
	require.Equal(t, "上游生图服务暂时不可用，请稍后重试。", webConsoleImageTaskErrorMessage(err))
}

func TestWebConsoleImageTaskErrorMessageHandlesTimeout(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://api.example.com/v1/responses", Err: context.DeadlineExceeded}

	require.Equal(t, "生图任务超时，请稍后重试或减少张数。", webConsoleImageTaskErrorMessage(err))
}

func TestFailTaskStoresUserFacingErrorMessage(t *testing.T) {
	repo := &webConsoleImageTaskRepoStub{}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	task := &service.WebConsoleImageTask{ID: 42, Status: "running"}

	h.failTask(context.Background(), task, fmt.Errorf("image task upstream failed: status 502: upstream_error: Upstream service temporarily unavailable"))

	require.Equal(t, "failed", repo.updated.Status)
	require.NotNil(t, repo.updated.CompletedAt)
	require.Equal(t, "上游生图服务暂时不可用，请稍后重试。", repo.updated.ErrorMessage)
}

func TestFailTaskStoresTimeoutErrorMessage(t *testing.T) {
	repo := &webConsoleImageTaskRepoStub{}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	task := &service.WebConsoleImageTask{ID: 43, Status: "running"}

	h.failTask(context.Background(), task, &url.Error{Op: "Post", URL: "https://api.example.com/v1/responses", Err: context.DeadlineExceeded})

	require.Equal(t, "failed", repo.updated.Status)
	require.NotNil(t, repo.updated.CompletedAt)
	require.Equal(t, "生图任务超时，请稍后重试或减少张数。", repo.updated.ErrorMessage)
}

func webConsoleTestContext() *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/web-console/image-tasks", nil)
	return c
}

func withWebConsoleHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	original := webConsoleHTTPClientFactory
	webConsoleHTTPClientFactory = func() (*http.Client, error) { return client, nil }
	t.Cleanup(func() { webConsoleHTTPClientFactory = original })
}

func webConsoleTestBase64Image(index int32) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(fmt.Sprintf("%02d", index), 33)))
}

type webConsoleImageTaskRepoStub struct {
	mu            sync.Mutex
	updated       *service.WebConsoleImageTask
	asset         *service.ImageGenerationAsset
	getAssetCalls int
}

func (r *webConsoleImageTaskRepoStub) CreateRecord(context.Context, *service.ImageGenerationRecord) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) UpdateRecord(context.Context, *service.ImageGenerationRecord) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) GetRecordByID(context.Context, int64) (*service.ImageGenerationRecord, []*service.ImageGenerationAsset, error) {
	return nil, nil, service.ErrImageGenerationRecordNotFound
}

func (r *webConsoleImageTaskRepoStub) ListRecords(context.Context, service.ImageGenerationRecordListParams) ([]*service.ImageGenerationRecord, *service.ImageGenerationRecordListResult, error) {
	return nil, nil, nil
}

func (r *webConsoleImageTaskRepoStub) ListDailyStats(context.Context, service.ImageGenerationRecordDailyStatsParams) ([]service.ImageGenerationDailyStat, error) {
	return nil, nil
}

func (r *webConsoleImageTaskRepoStub) GetStorageStats(context.Context) (service.ImageGenerationStorageStats, error) {
	return service.ImageGenerationStorageStats{}, nil
}

func (r *webConsoleImageTaskRepoStub) CreateAsset(context.Context, *service.ImageGenerationAsset) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) GetAssetByID(context.Context, int64) (*service.ImageGenerationAsset, *service.ImageGenerationRecord, error) {
	r.getAssetCalls++
	if r.asset != nil {
		return r.asset, &service.ImageGenerationRecord{ID: r.asset.RecordID}, nil
	}
	return nil, nil, service.ErrImageGenerationRecordNotFound
}

func (r *webConsoleImageTaskRepoStub) ListAssetsByRecordID(context.Context, int64) ([]*service.ImageGenerationAsset, error) {
	return nil, nil
}

func (r *webConsoleImageTaskRepoStub) CreateWebConsoleTask(context.Context, *service.WebConsoleImageTask) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) ClaimWebConsoleTask(context.Context, int64, time.Time) (*service.WebConsoleImageTask, bool, error) {
	return nil, false, nil
}

func (r *webConsoleImageTaskRepoStub) GetWebConsoleTaskByID(context.Context, int64) (*service.WebConsoleImageTask, error) {
	return nil, service.ErrWebConsoleImageTaskNotFound
}

func (r *webConsoleImageTaskRepoStub) ListWebConsoleTasksByUserID(context.Context, int64, pagination.PaginationParams) ([]*service.WebConsoleImageTask, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *webConsoleImageTaskRepoStub) MarkWebConsoleTasksUserDeletedBySessionID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

func (r *webConsoleImageTaskRepoStub) UpdateWebConsoleTask(_ context.Context, task *service.WebConsoleImageTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := *task
	r.updated = &copied
	return nil
}

func (r *webConsoleImageTaskRepoStub) CountDailyByDate(context.Context, time.Time) (int64, error) {
	return 0, nil
}
