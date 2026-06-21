//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
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

func TestWebConsoleUserAssetResponsesUseAuthenticatedAssetURL(t *testing.T) {
	imageService := service.NewImageGenerationArchiveService(nil, nil, nil, &config.Config{})
	h := &WebConsoleImageTaskHandler{imageService: imageService}

	assets := h.userAssetResponses(42, []*service.ImageGenerationAsset{{ID: 7, RecordID: 42, AssetIndex: 0, MimeType: "image/png", Extension: ".png", SHA256: "abc123"}})

	require.Len(t, assets, 1)
	rawURL, ok := assets[0]["url"].(string)
	require.True(t, ok)
	require.Equal(t, "/api/v1/web-console/image-tasks/assets/7", rawURL)
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
	signedURL := imageService.SignStableAssetURLPath("/api/v1/image-assets/7", 7, service.ImageAssetScopeAdmin, "abc123")
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
	signedURL := imageService.SignStableAssetURLPath("/api/v1/image-assets/7", 7, service.ImageAssetScopeAdmin, "oldhash")
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
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
}

func TestWebConsoleImageResponsesPayloadUsesConjureCompatibleResponsesShape(t *testing.T) {
	payload := webConsoleImageResponsesPayload(createWebConsoleImageTaskRequest{
		Model:  "gpt-5.5",
		Prompt: "画一只猫",
	}, webConsoleImageTaskOptions{
		Size:         "1024x1024",
		Quality:      "high",
		Background:   "opaque",
		OutputFormat: "webp",
	})

	require.Equal(t, "gpt-5.5", payload["model"])
	require.Equal(t, true, payload["stream"])
	require.Equal(t, false, payload["store"])
	require.Equal(t, map[string]any{"type": "image_generation"}, payload["tool_choice"])
	require.Equal(t, []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "画一只猫"},
			},
		},
	}, payload["input"])
	require.Equal(t, []any{
		map[string]any{
			"type":          "image_generation",
			"action":        "generate",
			"model":         "gpt-image-2",
			"size":          "1024x1024",
			"quality":       "high",
			"background":    "opaque",
			"output_format": "webp",
		},
	}, payload["tools"])
}

func TestWebConsoleImageResponsesPayloadSupportsEditReferencesAndMask(t *testing.T) {
	source := webConsoleTestPNGDataURL(t, 2, 2, false)
	mask := webConsoleTestPNGDataURL(t, 2, 2, true)
	compression := 80

	payload := webConsoleImageResponsesPayload(createWebConsoleImageTaskRequest{
		Mode:   "edit",
		Model:  "gpt-5.5",
		Prompt: "把背景换成海边",
		ReferenceImages: []webConsoleImageReference{{
			DataURL: source,
			Name:    "source.png",
		}},
		MaskImage: &webConsoleImageReference{DataURL: mask, Name: "mask.png"},
	}, webConsoleImageTaskOptions{
		Size:              "1536x1024",
		Quality:           "medium",
		OutputFormat:      "webp",
		Ratio:             "16:9",
		OutputCompression: &compression,
		InputFidelity:     "high",
	})

	require.Equal(t, "gpt-5.5", payload["model"])
	require.Equal(t, map[string]any{"type": "image_generation"}, payload["tool_choice"])
	require.NotContains(t, payload, "parallel_tool_calls")
	require.NotContains(t, payload, "instructions")
	require.Equal(t, []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "把背景换成海边\n\n将宽高比设为 16:9"},
				map[string]any{"type": "input_image", "image_url": source},
			},
		},
	}, payload["input"])
	require.Equal(t, []any{
		map[string]any{
			"type":               "image_generation",
			"action":             "edit",
			"model":              "gpt-image-2",
			"size":               "1536x1024",
			"quality":            "medium",
			"output_format":      "webp",
			"output_compression": 80,
			"input_image_mask":   map[string]any{"image_url": mask},
		},
	}, payload["tools"])
}

func TestWebConsoleImageResponsesPayloadOmitsUnsupportedImageToolOptions(t *testing.T) {
	compression := 80
	payload := webConsoleImageResponsesPayload(createWebConsoleImageTaskRequest{
		Model:  "gpt-5.5",
		Prompt: "画一只猫",
	}, webConsoleImageTaskOptions{
		OutputFormat:      "png",
		OutputCompression: &compression,
		InputFidelity:     "high",
	})

	tools := payload["tools"].([]any)
	imageTool := tools[0].(map[string]any)
	require.NotContains(t, imageTool, "input_fidelity")
	require.NotContains(t, imageTool, "output_compression")
}

func TestNormalizeWebConsoleImageTaskRequestValidatesEditInputs(t *testing.T) {
	req := createWebConsoleImageTaskRequest{Mode: "edit"}
	require.ErrorContains(t, normalizeWebConsoleImageTaskRequest(&req), "requires at least one reference image")

	req = createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{DataURL: "data:text/plain;base64,ZmFrZQ=="}},
	}
	require.ErrorContains(t, normalizeWebConsoleImageTaskRequest(&req), "data:image")

	req = createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{
			DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("source-image")),
			Name:    " source.png ",
		}},
	}
	require.NoError(t, normalizeWebConsoleImageTaskRequest(&req))
	require.Equal(t, "edit", req.Mode)
	require.Equal(t, "source.png", req.ReferenceImages[0].Name)
}

func TestNormalizeWebConsoleImageTaskRequestValidatesReferenceSizeAndMask(t *testing.T) {
	tooLarge := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, webConsoleImageReferenceMaxBytes+1))
	req := createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{DataURL: tooLarge}},
	}
	require.ErrorContains(t, normalizeWebConsoleImageTaskRequest(&req), "exceeds max size")

	source := webConsoleTestPNGDataURL(t, 2, 2, false)
	alphaMask := webConsoleTestPNGDataURL(t, 2, 2, true)
	req = createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{DataURL: source}},
		MaskImage:       &webConsoleImageReference{DataURL: alphaMask},
	}
	require.NoError(t, normalizeWebConsoleImageTaskRequest(&req))

	opaqueMask := webConsoleTestPNGDataURL(t, 2, 2, false)
	req = createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{DataURL: source}},
		MaskImage:       &webConsoleImageReference{DataURL: opaqueMask},
	}
	require.ErrorContains(t, normalizeWebConsoleImageTaskRequest(&req), "alpha channel")

	mismatchedMask := webConsoleTestPNGDataURL(t, 3, 2, true)
	req = createWebConsoleImageTaskRequest{
		ReferenceImages: []webConsoleImageReference{{DataURL: source}},
		MaskImage:       &webConsoleImageReference{DataURL: mismatchedMask},
	}
	require.ErrorContains(t, normalizeWebConsoleImageTaskRequest(&req), "same dimensions")
}

func TestWebConsoleUpstreamErrorDetailPrefersStructuredMessage(t *testing.T) {
	detail := webConsoleUpstreamErrorDetail([]byte(`{"error":{"type":"server_error","code":"bad_gateway","message":"upstream timed out"}}`))

	require.Equal(t, "bad_gateway: upstream timed out", detail)
}

func TestWebConsoleResponsesTerminalErrorDetailParsesFailedSSE(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"image_blocked","message":"policy rejected"}}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	require.Equal(t, "image_blocked: policy rejected", webConsoleResponsesTerminalErrorDetail([]byte(body)))
}

func TestCollectWebConsoleImageValuesParsesResponsesSSEAndDedupes(t *testing.T) {
	first := webConsoleTestBase64Image(1)
	second := webConsoleTestBase64Image(2)
	body := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"` + first + `"}}`,
		"",
		`data: {"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"` + first + `"},{"id":"ig_2","type":"image_generation_call","result":"` + second + `"}]}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	values := collectWebConsoleImageValues([]byte(body))

	require.Len(t, values, 2)
	require.Equal(t, first, values[0].Base64)
	require.Equal(t, second, values[1].Base64)
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

func TestRunWebConsoleImageRequestsPassesEditReferencesAndMask(t *testing.T) {
	source := webConsoleTestPNGDataURL(t, 2, 2, false)
	mask := webConsoleTestPNGDataURL(t, 2, 2, true)
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		received <- payload
		_, _ = w.Write([]byte(`{"output":[{"type":"image_generation_call","b64_json":"` + webConsoleTestBase64Image(1) + `"}]}`))
	}))
	defer server.Close()
	withWebConsoleHTTPClient(t, server.Client())

	images, err := runWebConsoleImageRequests(context.Background(), createWebConsoleImageTaskRequest{
		Mode:     "edit",
		Model:    "gpt-5.5",
		Prompt:   "replace the background",
		Endpoint: server.URL + "/v1",
		Options:  []byte(`{"count":1,"outputFormat":"png","inputFidelity":"high","outputCompression":80}`),
		ReferenceImages: []webConsoleImageReference{{
			DataURL: source,
			Name:    "source.png",
		}},
		MaskImage: &webConsoleImageReference{
			DataURL: mask,
			Name:    "mask.png",
		},
	}, "sk-test")

	require.NoError(t, err)
	require.Len(t, images, 1)
	payload := <-received
	require.Equal(t, "gpt-5.5", payload["model"])
	require.Equal(t, true, payload["stream"])
	input := payload["input"].([]any)
	content := input[0].(map[string]any)["content"].([]any)
	require.Equal(t, map[string]any{"type": "input_text", "text": "replace the background"}, content[0])
	require.Equal(t, map[string]any{"type": "input_image", "image_url": source}, content[1])
	tools := payload["tools"].([]any)
	imageTool := tools[0].(map[string]any)
	require.Equal(t, "edit", imageTool["action"])
	require.Equal(t, map[string]any{"image_url": mask}, imageTool["input_image_mask"])
	require.NotContains(t, imageTool, "input_fidelity")
	require.NotContains(t, imageTool, "output_compression")
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
	require.Equal(t, "上游生图服务暂时不可用：upstream_error: Upstream service temporarily unavailable", webConsoleImageTaskErrorMessage(err))
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
	require.Equal(t, "上游生图服务暂时不可用：upstream_error: Upstream service temporarily unavailable", repo.updated.ErrorMessage)
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

func TestWebConsoleGetTreatsArchivedAssetsAsCompleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recordID := int64(55)
	repo := &webConsoleImageTaskRepoStub{
		task: &service.WebConsoleImageTask{
			ID:           44,
			UserID:       9,
			Status:       "failed",
			RecordID:     &recordID,
			ErrorMessage: "previous polling error",
		},
		record: &service.ImageGenerationRecord{ID: recordID, UserID: ptrInt64ForWebConsoleTest(9)},
		assets: []*service.ImageGenerationAsset{{
			ID:         7,
			RecordID:   recordID,
			AssetIndex: 0,
			MimeType:   "image/png",
			Extension:  ".png",
			SHA256:     "hash",
		}},
	}
	h := &WebConsoleImageTaskHandler{
		imageService: service.NewImageGenerationArchiveService(repo, nil, nil, &config.Config{}),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "44"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/web-console/image-tasks/44", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9})

	h.Get(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, repo.updated)
	require.Equal(t, "completed", repo.updated.Status)
	require.Empty(t, repo.updated.ErrorMessage)
	require.NotNil(t, repo.updated.CompletedAt)
	require.Contains(t, rec.Body.String(), `"/api/v1/web-console/image-tasks/assets/7"`)
	require.Contains(t, rec.Body.String(), `"status":"completed"`)
}

func ptrInt64ForWebConsoleTest(v int64) *int64 {
	return &v
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

func webConsoleTestPNGDataURL(t *testing.T, width, height int, withAlpha bool) string {
	t.Helper()
	var img image.Image
	if withAlpha {
		rgba := image.NewNRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				rgba.SetNRGBA(x, y, color.NRGBA{R: 255, A: 128})
			}
		}
		img = rgba
	} else {
		gray := image.NewGray(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				gray.SetGray(x, y, color.Gray{Y: 180})
			}
		}
		img = gray
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

type webConsoleImageTaskRepoStub struct {
	mu            sync.Mutex
	updated       *service.WebConsoleImageTask
	task          *service.WebConsoleImageTask
	record        *service.ImageGenerationRecord
	assets        []*service.ImageGenerationAsset
	asset         *service.ImageGenerationAsset
	getAssetCalls int
}

func (r *webConsoleImageTaskRepoStub) CreateRecord(context.Context, *service.ImageGenerationRecord) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) UpdateRecord(context.Context, *service.ImageGenerationRecord) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) GetRecordByID(_ context.Context, id int64) (*service.ImageGenerationRecord, []*service.ImageGenerationAsset, error) {
	if r.record != nil && r.record.ID == id {
		return r.record, r.assets, nil
	}
	return nil, nil, service.ErrImageGenerationRecordNotFound
}

func (r *webConsoleImageTaskRepoStub) ListRecords(context.Context, service.ImageGenerationRecordListParams) ([]*service.ImageGenerationRecord, *service.ImageGenerationRecordListResult, error) {
	return nil, nil, nil
}

func (r *webConsoleImageTaskRepoStub) ListAllArchiveStorageRefs(context.Context) (*service.ImageGenerationArchiveClearResult, error) {
	return &service.ImageGenerationArchiveClearResult{}, nil
}

func (r *webConsoleImageTaskRepoStub) DeleteArchiveRecordsByID(context.Context, []int64) (int64, error) {
	return 0, nil
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

func (r *webConsoleImageTaskRepoStub) ListAssetsByRecordID(_ context.Context, id int64) ([]*service.ImageGenerationAsset, error) {
	if r.record != nil && r.record.ID == id {
		return r.assets, nil
	}
	return nil, nil
}

func (r *webConsoleImageTaskRepoStub) CreateWebConsoleTask(context.Context, *service.WebConsoleImageTask) error {
	return nil
}

func (r *webConsoleImageTaskRepoStub) ClaimWebConsoleTask(context.Context, int64, time.Time) (*service.WebConsoleImageTask, bool, error) {
	return nil, false, nil
}

func (r *webConsoleImageTaskRepoStub) GetWebConsoleTaskByID(_ context.Context, id int64) (*service.WebConsoleImageTask, error) {
	if r.task != nil && r.task.ID == id {
		copied := *r.task
		return &copied, nil
	}
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
