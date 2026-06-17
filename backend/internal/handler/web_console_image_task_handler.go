package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
)

type WebConsoleImageTaskHandler struct {
	imageService   *service.ImageGenerationArchiveService
	apiKeyService  *service.APIKeyService
	settingService *service.SettingService
	runningTasks   sync.Map
}

const (
	webConsoleImageTaskLease                  = 10 * time.Minute
	webConsoleImageRequestTimeout             = 10 * time.Minute
	webConsoleImageResponseHeaderTimeout      = 0
	webConsoleImageRequestMaxIdleConnsPerHost = 4
	webConsoleImageRequestMaxConnsPerHost     = 8
	webConsoleCodexUserAgent                  = "codex_cli_rs/0.125.0 (Ubuntu 22.4.0; x86_64) xterm-256color"
	webConsoleCodexOriginator                 = "codex_cli_rs"
	webConsoleCodexVersion                    = "0.125.0"
)

func NewWebConsoleImageTaskHandler(imageService *service.ImageGenerationArchiveService, apiKeyService *service.APIKeyService, settingService *service.SettingService) *WebConsoleImageTaskHandler {
	return &WebConsoleImageTaskHandler{imageService: imageService, apiKeyService: apiKeyService, settingService: settingService}
}

type createWebConsoleImageTaskRequest struct {
	APIKeyID  int64           `json:"api_key_id"`
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt"`
	Options   json.RawMessage `json:"options"`
	SessionID string          `json:"session_id"`
	MessageID string          `json:"message_id"`
	Endpoint  string          `json:"endpoint"`
}

type webConsoleImageTaskOptions struct {
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	Background   string `json:"background"`
	OutputFormat string `json:"outputFormat"`
	Count        int    `json:"count"`
}

type webConsoleImageTaskSnapshot struct {
	Version          int             `json:"version"`
	APIKeyID         int64           `json:"api_key_id"`
	Model            string          `json:"model"`
	Prompt           string          `json:"prompt"`
	Options          json.RawMessage `json:"options,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	MessageID        string          `json:"message_id,omitempty"`
	Endpoint         string          `json:"endpoint"`
	ResolvedEndpoint string          `json:"resolved_endpoint"`
}

func (h *WebConsoleImageTaskHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	var req createWebConsoleImageTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.APIKeyID <= 0 || req.Model == "" || req.Prompt == "" {
		response.BadRequest(c, "api_key_id, model and prompt are required")
		return
	}
	enabled, err := h.imageService.IsArchiveEnabled(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !enabled {
		response.ErrorFrom(c, service.ErrImageArchiveDisabled)
		return
	}
	resolvedEndpoint, err := h.authorizeWebConsoleEndpoint(c, req.Endpoint)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), req.APIKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if apiKey == nil || apiKey.UserID != subject.UserID {
		response.Forbidden(c, "api key does not belong to current user")
		return
	}
	if err := validateWebConsoleAPIKey(apiKey); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	snapshot := webConsoleImageTaskSnapshot{
		Version:          1,
		APIKeyID:         req.APIKeyID,
		Model:            req.Model,
		Prompt:           req.Prompt,
		Options:          normalizeWebConsoleOptionsRaw(req.Options),
		SessionID:        req.SessionID,
		MessageID:        req.MessageID,
		Endpoint:         req.Endpoint,
		ResolvedEndpoint: resolvedEndpoint,
	}
	requestJSON, err := json.Marshal(snapshot)
	if err != nil {
		response.BadRequest(c, "invalid image task request")
		return
	}
	task := &service.WebConsoleImageTask{
		UserID:      subject.UserID,
		APIKeyID:    &req.APIKeyID,
		SessionID:   req.SessionID,
		MessageID:   req.MessageID,
		Status:      "pending",
		RequestJSON: requestJSON,
	}
	if err := h.imageService.CreateWebConsoleTask(c.Request.Context(), task); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.startWebConsoleImageTask(task.ID, subject.UserID, snapshot)
	response.Success(c, gin.H{"task": webConsoleTaskResponse(task, nil)})
}

func (h *WebConsoleImageTaskHandler) Get(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid task id")
		return
	}
	task, err := h.imageService.GetWebConsoleTaskByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if task.UserID != subject.UserID {
		response.Forbidden(c, "task does not belong to current user")
		return
	}
	if task.UserDeletedAt != nil {
		response.ErrorFrom(c, service.ErrWebConsoleImageTaskNotFound)
		return
	}
	h.resumeWebConsoleImageTaskIfNeeded(task)
	assets := []gin.H{}
	if task.RecordID != nil {
		if _, imageAssets, err := h.imageService.GetRecordByID(c.Request.Context(), *task.RecordID); err == nil {
			assets = h.userAssetResponses(*task.RecordID, imageAssets)
		}
	}
	response.Success(c, webConsoleTaskResponse(task, assets))
}

func (h *WebConsoleImageTaskHandler) DeleteSession(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		response.BadRequest(c, "invalid session id")
		return
	}
	deleted, err := h.imageService.MarkWebConsoleTasksUserDeletedBySessionID(c.Request.Context(), subject.UserID, sessionID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": deleted})
}

func (h *WebConsoleImageTaskHandler) GetAsset(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(c.Param("asset_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid asset id")
		return
	}
	asset, record, err := h.imageService.GetAssetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if record.UserID == nil || *record.UserID != subject.UserID {
		response.Forbidden(c, "image asset does not belong to current user")
		return
	}
	reader, err := h.imageService.OpenAsset(c.Request.Context(), asset)
	if err != nil {
		response.NotFound(c, "image asset is not available")
		return
	}
	writeImageAssetReader(c, reader)
}

func (h *WebConsoleImageTaskHandler) GetSignedAsset(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("asset_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid asset id")
		return
	}
	scope := strings.TrimSpace(c.Query("scope"))
	if scope != service.ImageAssetScopeWebConsole && scope != service.ImageAssetScopeAdmin {
		response.Unauthorized(c, "invalid image asset token")
		return
	}
	version := strings.TrimSpace(c.Query("v"))
	if version != "" {
		if !h.imageService.VerifyStableAssetToken(id, scope, version, c.Query("expires"), c.Query("sig"), time.Now().UTC()) {
			response.Unauthorized(c, "invalid image asset token")
			return
		}
	} else if !h.imageService.VerifyAssetToken(id, scope, c.Query("expires"), c.Query("sig"), time.Now().UTC()) {
		response.Unauthorized(c, "invalid image asset token")
		return
	}
	asset, _, err := h.imageService.GetAssetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if version != "" && version != imageAssetVersion(asset) {
		response.Unauthorized(c, "invalid image asset token")
		return
	}
	reader, err := h.imageService.OpenAsset(c.Request.Context(), asset)
	if err != nil {
		response.NotFound(c, "image asset is not available")
		return
	}
	writeImageAssetReader(c, reader)
}

func (h *WebConsoleImageTaskHandler) startWebConsoleImageTask(taskID, userID int64, snapshot webConsoleImageTaskSnapshot) {
	if taskID <= 0 {
		return
	}
	if _, loaded := h.runningTasks.LoadOrStore(taskID, struct{}{}); loaded {
		return
	}
	go func() {
		defer h.runningTasks.Delete(taskID)
		h.runWebConsoleImageTask(taskID, userID, snapshot)
	}()
}

func (h *WebConsoleImageTaskHandler) resumeWebConsoleImageTaskIfNeeded(task *service.WebConsoleImageTask) {
	if task == nil || (task.Status != "pending" && task.Status != "running") {
		return
	}
	snapshot, err := webConsoleTaskSnapshot(task)
	if err != nil {
		h.failTask(context.Background(), task, err)
		return
	}
	h.startWebConsoleImageTask(task.ID, task.UserID, snapshot)
}

func (h *WebConsoleImageTaskHandler) runWebConsoleImageTask(taskID, userID int64, snapshot webConsoleImageTaskSnapshot) {
	ctx := context.Background()
	task, claimed, err := h.imageService.ClaimWebConsoleTask(ctx, taskID, time.Now().UTC().Add(-webConsoleImageTaskLease))
	if err != nil {
		return
	}
	if !claimed || task == nil {
		return
	}
	if task.UserDeletedAt != nil {
		return
	}
	apiKey, err := h.apiKeyService.GetByID(ctx, snapshot.APIKeyID)
	if err != nil {
		h.failTask(ctx, task, err)
		return
	}
	if apiKey == nil || apiKey.UserID != userID {
		h.failTask(ctx, task, fmt.Errorf("api key does not belong to current user"))
		return
	}
	if err := validateWebConsoleAPIKey(apiKey); err != nil {
		h.failTask(ctx, task, err)
		return
	}
	if task.RecordID != nil {
		if record, assets, err := h.imageService.GetRecordByID(ctx, *task.RecordID); err == nil && record != nil && len(assets) > 0 {
			task.Status = "completed"
			completed := time.Now().UTC()
			task.CompletedAt = &completed
			task.ErrorMessage = ""
			_ = h.imageService.UpdateWebConsoleTask(ctx, task)
			return
		}
	}

	record, err := h.ensureWebConsoleRecord(ctx, task, userID, snapshot)
	if err != nil {
		h.failTask(ctx, task, err)
		return
	}
	req := createWebConsoleImageTaskRequest{
		APIKeyID:  snapshot.APIKeyID,
		Model:     snapshot.Model,
		Prompt:    snapshot.Prompt,
		Options:   snapshot.Options,
		SessionID: snapshot.SessionID,
		MessageID: snapshot.MessageID,
		Endpoint:  snapshot.ResolvedEndpoint,
	}
	images, err := runWebConsoleImageRequests(ctx, req, apiKey.Key)
	if err != nil {
		h.failTask(ctx, task, err)
		return
	}
	if err := h.imageService.ArchiveImageBytesSync(ctx, record, images); err != nil {
		h.failTask(ctx, task, err)
		return
	}
	task.Status = "completed"
	completed := time.Now().UTC()
	task.CompletedAt = &completed
	task.ErrorMessage = ""
	_ = h.imageService.UpdateWebConsoleTask(ctx, task)
}

func (h *WebConsoleImageTaskHandler) ensureWebConsoleRecord(ctx context.Context, task *service.WebConsoleImageTask, userID int64, snapshot webConsoleImageTaskSnapshot) (*service.ImageGenerationRecord, error) {
	if task.RecordID != nil {
		if record, _, err := h.imageService.GetRecordByID(ctx, *task.RecordID); err == nil && record != nil {
			return record, nil
		}
	}
	record := &service.ImageGenerationRecord{
		UserID:        &userID,
		APIKeyID:      &snapshot.APIKeyID,
		Source:        "web_console",
		Endpoint:      "/v1/responses",
		Model:         strings.TrimSpace(snapshot.Model),
		PromptExcerpt: snapshot.Prompt,
		Status:        "pending",
	}
	if err := h.imageService.CreateRecord(ctx, record); err != nil {
		return nil, err
	}
	task.RecordID = &record.ID
	if err := h.imageService.UpdateWebConsoleTask(ctx, task); err != nil {
		return nil, err
	}
	return record, nil
}

func (h *WebConsoleImageTaskHandler) failTask(ctx context.Context, task *service.WebConsoleImageTask, err error) {
	if task == nil || err == nil {
		return
	}
	task.Status = "failed"
	task.ErrorMessage = webConsoleImageTaskErrorMessage(err)
	completed := time.Now().UTC()
	task.CompletedAt = &completed
	_ = h.imageService.UpdateWebConsoleTask(ctx, task)
}

func webConsoleTaskResponse(task *service.WebConsoleImageTask, assets []gin.H) gin.H {
	return gin.H{
		"id":            task.ID,
		"user_id":       task.UserID,
		"api_key_id":    task.APIKeyID,
		"session_id":    task.SessionID,
		"message_id":    task.MessageID,
		"status":        task.Status,
		"record_id":     task.RecordID,
		"error_message": task.ErrorMessage,
		"created_at":    task.CreatedAt,
		"started_at":    task.StartedAt,
		"completed_at":  task.CompletedAt,
		"updated_at":    task.UpdatedAt,
		"assets":        assets,
	}
}

func (h *WebConsoleImageTaskHandler) userAssetResponses(recordID int64, assets []*service.ImageGenerationAsset) []gin.H {
	out := make([]gin.H, 0, len(assets))
	for _, asset := range assets {
		rawURL := "/api/v1/image-assets/" + strconv.FormatInt(asset.ID, 10)
		url := rawURL
		if h != nil && h.imageService != nil {
			url = h.imageService.SignStableAssetURLPath(rawURL, asset.ID, service.ImageAssetScopeWebConsole, imageAssetVersion(asset))
		}
		out = append(out, gin.H{
			"id": asset.ID, "record_id": recordID, "asset_index": asset.AssetIndex, "mime_type": asset.MimeType,
			"extension": asset.Extension, "width": asset.Width, "height": asset.Height, "bytes": asset.Bytes,
			"sha256": asset.SHA256, "url": url,
		})
	}
	return out
}

func imageAssetVersion(asset *service.ImageGenerationAsset) string {
	if asset == nil {
		return ""
	}
	if strings.TrimSpace(asset.SHA256) != "" {
		return strings.TrimSpace(asset.SHA256)
	}
	return strconv.FormatInt(asset.Bytes, 10) + "-" + asset.CreatedAt.UTC().Format(time.RFC3339Nano)
}

func webConsoleTaskSnapshot(task *service.WebConsoleImageTask) (webConsoleImageTaskSnapshot, error) {
	if task == nil || len(task.RequestJSON) == 0 {
		return webConsoleImageTaskSnapshot{}, fmt.Errorf("image task request snapshot is not available; please retry")
	}
	var snapshot webConsoleImageTaskSnapshot
	if err := json.Unmarshal(task.RequestJSON, &snapshot); err != nil {
		return webConsoleImageTaskSnapshot{}, fmt.Errorf("image task request snapshot is invalid; please retry")
	}
	if snapshot.APIKeyID <= 0 && task.APIKeyID != nil {
		snapshot.APIKeyID = *task.APIKeyID
	}
	snapshot.Model = strings.TrimSpace(snapshot.Model)
	snapshot.Prompt = strings.TrimSpace(snapshot.Prompt)
	snapshot.Endpoint = strings.TrimSpace(snapshot.Endpoint)
	snapshot.ResolvedEndpoint = strings.TrimSpace(snapshot.ResolvedEndpoint)
	if snapshot.APIKeyID <= 0 || snapshot.Model == "" || snapshot.Prompt == "" || snapshot.ResolvedEndpoint == "" {
		return webConsoleImageTaskSnapshot{}, fmt.Errorf("image task request snapshot is incomplete; please retry")
	}
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	snapshot.MessageID = strings.TrimSpace(snapshot.MessageID)
	snapshot.Options = normalizeWebConsoleOptionsRaw(snapshot.Options)
	return snapshot, nil
}

func normalizeWebConsoleOptionsRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	copied := make(json.RawMessage, len(raw))
	copy(copied, raw)
	return copied
}

func validateWebConsoleAPIKey(apiKey *service.APIKey) error {
	if apiKey == nil {
		return fmt.Errorf("api key is not available")
	}
	if !apiKey.IsActive() {
		return fmt.Errorf("api key is disabled")
	}
	if apiKey.IsExpired() {
		return fmt.Errorf("api key is expired")
	}
	if apiKey.IsQuotaExhausted() {
		return fmt.Errorf("api key quota is exhausted")
	}
	return nil
}

func writeImageAssetReader(c *gin.Context, reader *service.ImageGenerationAssetReader) {
	if reader == nil || reader.Body == nil {
		response.NotFound(c, "image asset is not available")
		return
	}
	defer func() { _ = reader.Body.Close() }()
	c.DataFromReader(http.StatusOK, reader.Size, reader.ContentType, reader.Body, map[string]string{
		"Content-Disposition": "inline; filename=\"" + reader.Filename + "\"",
		"Cache-Control":       "private, max-age=300",
	})
}

func runWebConsoleImageRequests(ctx context.Context, req createWebConsoleImageTaskRequest, apiKey string) ([]service.ArchivedImageBytesInput, error) {
	options := webConsoleImageTaskOptions{OutputFormat: "png", Count: 1}
	if len(req.Options) > 0 {
		_ = json.Unmarshal(req.Options, &options)
	}
	if options.Count <= 0 {
		options.Count = 1
	}
	if options.Count > 4 {
		options.Count = 4
	}
	client, err := webConsoleHTTPClientFactory()
	if err != nil {
		return nil, err
	}
	endpoint, err := webConsoleEndpointURL(req.Endpoint, "/responses")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type imageRequestResult struct {
		index  int
		images []service.ArchivedImageBytesInput
		err    error
	}
	results := make(chan imageRequestResult, options.Count)
	for i := 0; i < options.Count; i++ {
		go func(index int) {
			images, err := runSingleWebConsoleImageRequest(ctx, client, endpoint, req, options, apiKey)
			results <- imageRequestResult{index: index, images: images, err: err}
		}(i)
	}
	ordered := make([][]service.ArchivedImageBytesInput, options.Count)
	for i := 0; i < options.Count; i++ {
		result := <-results
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		ordered[result.index] = result.images
	}
	out := make([]service.ArchivedImageBytesInput, 0, options.Count)
	for _, batch := range ordered {
		for _, image := range batch {
			image.Index = len(out)
			out = append(out, image)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Responses did not return any image")
	}
	return out, nil
}

func runSingleWebConsoleImageRequest(ctx context.Context, client *http.Client, endpoint string, req createWebConsoleImageTaskRequest, options webConsoleImageTaskOptions, apiKey string) ([]service.ArchivedImageBytesInput, error) {
	payload := webConsoleImageResponsesPayload(req, options)
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	applyWebConsoleCodexRequestHeaders(httpReq)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode >= 400 {
		if detail := webConsoleUpstreamErrorDetail(body); detail != "" {
			return nil, fmt.Errorf("image task upstream failed: status %d: %s", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("image task upstream failed: status %d", resp.StatusCode)
	}
	collected := collectWebConsoleImageValues(body)
	out := make([]service.ArchivedImageBytesInput, 0, len(collected))
	for _, item := range collected {
		imageBytes, mimeType, ext, err := imageValueToBytes(ctx, client, item)
		if err != nil {
			return nil, err
		}
		out = append(out, service.ArchivedImageBytesInput{
			Index:     len(out),
			Bytes:     imageBytes,
			MimeType:  mimeType,
			Extension: ext,
		})
	}
	return out, nil
}

func webConsoleImageTaskErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "生图任务失败，请稍后重试。"
	}
	if strings.Contains(message, "context deadline exceeded") || strings.Contains(message, "Client.Timeout exceeded") {
		return "生图任务超时，请稍后重试或减少张数。"
	}
	if strings.Contains(message, "image task upstream failed") {
		return "上游生图服务暂时不可用，请稍后重试。"
	}
	if strings.Contains(message, "Responses did not return any image") {
		return "上游未返回图片，请稍后重试。"
	}
	return message
}

func applyWebConsoleCodexRequestHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", webConsoleCodexUserAgent)
	req.Header.Set("originator", webConsoleCodexOriginator)
	req.Header.Set("version", webConsoleCodexVersion)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "application/json")
}

func webConsoleUpstreamErrorDetail(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var decoded struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil {
		message := strings.TrimSpace(decoded.Error.Message)
		code := strings.TrimSpace(decoded.Error.Code)
		errorType := strings.TrimSpace(decoded.Error.Type)
		switch {
		case code != "" && message != "":
			return code + ": " + message
		case errorType != "" && message != "":
			return errorType + ": " + message
		case message != "":
			return message
		case code != "":
			return code
		case errorType != "":
			return errorType
		}
	}
	const maxDetailBytes = 2048
	if len(trimmed) > maxDetailBytes {
		return trimmed[:maxDetailBytes] + "..."
	}
	return trimmed
}

func webConsoleImageResponsesPayload(req createWebConsoleImageTaskRequest, options webConsoleImageTaskOptions) map[string]any {
	tool := map[string]any{
		"type":  "image_generation",
		"model": "gpt-image-2",
	}
	if strings.TrimSpace(options.Size) != "" {
		tool["size"] = strings.TrimSpace(options.Size)
	}
	if strings.TrimSpace(options.Quality) != "" {
		tool["quality"] = strings.TrimSpace(options.Quality)
	}
	if strings.TrimSpace(options.Background) != "" && strings.TrimSpace(options.Background) != "transparent" {
		tool["background"] = strings.TrimSpace(options.Background)
	}
	if strings.TrimSpace(options.OutputFormat) != "" && strings.TrimSpace(options.OutputFormat) != "png" {
		tool["output_format"] = strings.TrimSpace(options.OutputFormat)
	}
	return map[string]any{
		"model":       strings.TrimSpace(req.Model),
		"input":       strings.TrimSpace(req.Prompt),
		"tools":       []any{tool},
		"tool_choice": map[string]any{"type": "image_generation"},
		"stream":      false,
	}
}

func webConsoleHTTPClient() (*http.Client, error) {
	return httpclient.GetClient(webConsoleHTTPClientOptions())
}

var webConsoleHTTPClientFactory = webConsoleHTTPClient

func webConsoleHTTPClientOptions() httpclient.Options {
	return httpclient.Options{
		Timeout:               webConsoleImageRequestTimeout,
		ResponseHeaderTimeout: webConsoleImageResponseHeaderTimeout,
		ValidateResolvedIP:    true,
		MaxIdleConnsPerHost:   webConsoleImageRequestMaxIdleConnsPerHost,
		MaxConnsPerHost:       webConsoleImageRequestMaxConnsPerHost,
	}
}

func (h *WebConsoleImageTaskHandler) authorizeWebConsoleEndpoint(c *gin.Context, rawEndpoint string) (string, error) {
	resolved, err := normalizeWebConsoleEndpointBase(c, rawEndpoint)
	if err != nil {
		return "", err
	}
	allowed, err := h.allowedWebConsoleEndpoints(c)
	if err != nil {
		return "", err
	}
	for _, candidate := range allowed {
		if sameWebConsoleEndpointBase(resolved, candidate) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("endpoint is not allowed")
}

func (h *WebConsoleImageTaskHandler) allowedWebConsoleEndpoints(c *gin.Context) ([]string, error) {
	out := make([]string, 0, 4)
	add := func(value string) {
		normalized, err := normalizeWebConsoleEndpointBase(c, value)
		if err != nil || normalized == "" {
			return
		}
		for _, existing := range out {
			if sameWebConsoleEndpointBase(existing, normalized) {
				return
			}
		}
		out = append(out, normalized)
	}
	if h != nil && h.settingService != nil {
		settings, err := h.settingService.GetPublicSettings(c.Request.Context())
		if err != nil {
			return nil, err
		}
		if settings != nil {
			add(settings.APIBaseURL)
			add(settings.WebConsoleDefaultEndpoint)
			for _, endpoint := range dto.ParseCustomEndpoints(settings.CustomEndpoints) {
				add(endpoint.Endpoint)
			}
		}
	}
	return out, nil
}

func normalizeWebConsoleEndpointBase(c *gin.Context, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("endpoint is required")
	}
	baseURL, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("endpoint must be absolute")
	}
	if baseURL.User != nil {
		return "", fmt.Errorf("endpoint must not include credentials")
	}
	scheme := strings.ToLower(baseURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("endpoint scheme is not allowed")
	}
	if _, err := urlvalidator.ValidateHTTPURL(baseURL.String(), true, urlvalidator.ValidationOptions{AllowPrivate: false}); err != nil {
		return "", err
	}
	trimmed := strings.TrimRight(baseURL.EscapedPath(), "/")
	if trimmed == "" || !strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		trimmed += "/v1"
	}
	baseURL.Path = trimmed
	baseURL.RawPath = ""
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return strings.TrimRight(baseURL.String(), "/"), nil
}

func sameWebConsoleEndpointBase(left, right string) bool {
	l, err := normalizeParsedWebConsoleEndpointBase(left)
	if err != nil {
		return false
	}
	r, err := normalizeParsedWebConsoleEndpointBase(right)
	if err != nil {
		return false
	}
	return l == r
}

func normalizeParsedWebConsoleEndpointBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("endpoint must be absolute")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	trimmed := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(trimmed, "/v1") {
		trimmed += "/v1"
	}
	u.Path = trimmed
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func webConsoleEndpointURL(base, path string) (string, error) {
	normalized, err := normalizeParsedWebConsoleEndpointBase(base)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

type webConsoleImageValue struct {
	Base64 string
	URL    string
	Mime   string
}

func collectWebConsoleImageValues(body []byte) []webConsoleImageValue {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	out := make([]webConsoleImageValue, 0)
	walkWebConsoleImageValues(decoded, &out)
	return out
}

func walkWebConsoleImageValues(node any, out *[]webConsoleImageValue) {
	switch v := node.(type) {
	case map[string]any:
		mimeType, _ := v["mime_type"].(string)
		if s, ok := v["b64_json"].(string); ok && strings.TrimSpace(s) != "" {
			*out = append(*out, webConsoleImageValue{Base64: s, Mime: mimeType})
		}
		if s, ok := v["result"].(string); ok && looksLikeBase64Image(s) {
			*out = append(*out, webConsoleImageValue{Base64: s, Mime: mimeType})
		}
		for _, key := range []string{"url", "image_url"} {
			if s, ok := v[key].(string); ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "http") {
				*out = append(*out, webConsoleImageValue{URL: s, Mime: mimeType})
			}
		}
		for _, child := range v {
			walkWebConsoleImageValues(child, out)
		}
	case []any:
		for _, child := range v {
			walkWebConsoleImageValues(child, out)
		}
	}
}

func looksLikeBase64Image(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 64 || strings.HasPrefix(strings.ToLower(s), "http") {
		return false
	}
	if strings.HasPrefix(strings.ToLower(s), "data:image/") {
		return true
	}
	if idx := strings.Index(s, ","); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimRight(s, "=") + strings.Repeat("=", (4-len(s)%4)%4)
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}

func imageValueToBytes(ctx context.Context, client *http.Client, value webConsoleImageValue) ([]byte, string, string, error) {
	if strings.TrimSpace(value.Base64) != "" {
		raw := strings.TrimSpace(value.Base64)
		if idx := strings.Index(raw, ","); idx >= 0 {
			raw = raw[idx+1:]
		}
		raw = strings.TrimRight(raw, "=") + strings.Repeat("=", (4-len(raw)%4)%4)
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, "", "", err
		}
		if len(b) > service.ImageArchiveMaxBytes {
			return nil, "", "", fmt.Errorf("image exceeds max archive size: %d bytes", len(b))
		}
		mimeType := strings.TrimSpace(value.Mime)
		if mimeType == "" {
			mimeType = "image/png"
		}
		return b, mimeType, extFromMime(mimeType), nil
	}
	if strings.TrimSpace(value.URL) == "" {
		return nil, "", "", fmt.Errorf("image output is empty")
	}
	downloadURL, err := urlvalidator.ValidateHTTPURL(strings.TrimSpace(value.URL), true, urlvalidator.ValidationOptions{AllowPrivate: false})
	if err != nil {
		return nil, "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, "", "", fmt.Errorf("download image failed: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, service.ImageArchiveMaxBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(b) > service.ImageArchiveMaxBytes {
		return nil, "", "", fmt.Errorf("image exceeds max archive size: %d bytes", len(b))
	}
	mimeType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	if mimeType == "" {
		mimeType = strings.TrimSpace(value.Mime)
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	return b, mimeType, extFromMimeOrURL(mimeType, value.URL), nil
}

func extFromMime(mimeType string) string {
	exts, _ := mime.ExtensionsByType(mimeType)
	if len(exts) > 0 {
		return exts[0]
	}
	if mimeType == "image/jpeg" {
		return ".jpg"
	}
	if mimeType == "image/webp" {
		return ".webp"
	}
	return ".png"
}

func extFromMimeOrURL(mimeType, rawURL string) string {
	if ext := filepath.Ext(rawURL); ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" {
		return ext
	}
	return extFromMime(mimeType)
}
