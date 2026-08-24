package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	appTimezone "github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ImageGenerationHandler provides a read-only view of the upstream async image bucket.
type ImageGenerationHandler struct {
	settings *service.ImageStorageSettingService
	factory  service.ImageStorageBrowserFactory
	tasks    *service.ImageTaskService
}

func NewImageGenerationHandler(settings *service.ImageStorageSettingService, factory service.ImageStorageBrowserFactory, tasks *service.ImageTaskService) *ImageGenerationHandler {
	return &ImageGenerationHandler{settings: settings, factory: factory, tasks: tasks}
}

func (h *ImageGenerationHandler) List(c *gin.Context) {
	if h == nil || h.settings == nil || h.factory == nil {
		response.BadRequest(c, "async image object storage is unavailable")
		return
	}
	cfg, err := h.settings.BrowserConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	prefix := strings.TrimSpace(c.Query("prefix"))
	if prefix == "" {
		prefix = cfg.Prefix
	} else if !strings.HasPrefix(prefix, cfg.Prefix) {
		response.BadRequest(c, "prefix must stay within the configured async image prefix")
		return
	}
	browser, err := h.factory(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	page, err := browser.List(c.Request.Context(), prefix, c.Query("cursor"), intQuery(c, "limit", 60))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"items":       page.Items,
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
		"total_count": page.TotalCount,
		"prefix":      cfg.Prefix,
		"bucket":      cfg.Bucket,
	})
}

type imageGenerationTaskView struct {
	ID          string   `json:"id"`
	TaskID      string   `json:"task_id"`
	UserID      int64    `json:"user_id"`
	APIKeyID    int64    `json:"api_key_id"`
	Platform    string   `json:"platform,omitempty"`
	Operation   string   `json:"operation,omitempty"`
	Model       string   `json:"model,omitempty"`
	ImageCount  int      `json:"image_count,omitempty"`
	ResultCount int      `json:"result_count"`
	Status      string   `json:"status"`
	HTTPStatus  int      `json:"http_status,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	CompletedAt *int64   `json:"completed_at,omitempty"`
	ExpiresAt   int64    `json:"expires_at"`
	DurationMs  int64    `json:"duration_ms"`
	ErrorType   string   `json:"error_type,omitempty"`
	StopReason  string   `json:"stop_reason,omitempty"`
	ResultURLs  []string `json:"result_urls,omitempty"`
}

func (h *ImageGenerationHandler) ListTasks(c *gin.Context) {
	if h == nil || h.tasks == nil {
		response.BadRequest(c, "async image task storage is unavailable")
		return
	}
	startAt, endAt, err := imageTaskDateRange(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	page, err := h.tasks.ListAdmin(c.Request.Context(), service.ImageTaskAdminQuery{
		Status:  c.Query("status"),
		Cursor:  c.Query("cursor"),
		Limit:   intQuery(c, "limit", 50),
		StartAt: startAt,
		EndAt:   endAt,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	now := time.Now().UTC()
	resultURLs, err := h.resolveTaskResultURLs(c.Request.Context(), page.Tasks)
	if err != nil {
		// History metadata remains useful when object storage is temporarily
		// unavailable. Durable-key tasks intentionally keep URLs empty instead of
		// exposing stale or cross-bucket links; legacy inline results remain usable.
		slog.Warn("resolve image task history URLs", "error", err)
		resultURLs = map[string][]string{}
	}
	items := make([]imageGenerationTaskView, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		items = append(items, imageGenerationTaskToView(task, now, resultURLs[task.ID]))
	}
	response.Success(c, gin.H{
		"items":       items,
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
		"stats":       page.Stats,
		"server_time": now.UnixMilli(),
	})
}

func (h *ImageGenerationHandler) resolveTaskResultURLs(ctx context.Context, tasks []*service.ImageTaskRecord) (map[string][]string, error) {
	if h == nil || h.settings == nil || h.factory == nil {
		for _, task := range tasks {
			if task != nil && len(task.ResultObjectKeys) > 0 {
				return nil, fmt.Errorf("resolve image task history URLs: object storage is unavailable")
			}
		}
		return map[string][]string{}, nil
	}
	cfg, err := h.settings.BrowserConfig(ctx)
	if err != nil {
		return nil, err
	}
	currentBinding := service.ImageStorageBindingID(cfg)
	keysByTask := make(map[string][]string)
	uniqueKeys := make([]string, 0)
	seen := make(map[string]struct{})
	for _, task := range tasks {
		if task == nil || len(task.ResultObjectKeys) == 0 || task.StorageBindingID == "" || task.StorageBindingID != currentBinding {
			continue
		}
		for _, rawKey := range task.ResultObjectKeys {
			key := strings.TrimSpace(rawKey)
			if key == "" {
				continue
			}
			keysByTask[task.ID] = append(keysByTask[task.ID], key)
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				uniqueKeys = append(uniqueKeys, key)
			}
		}
	}
	if len(uniqueKeys) == 0 {
		return map[string][]string{}, nil
	}
	for _, key := range uniqueKeys {
		if !strings.HasPrefix(key, cfg.Prefix) {
			return nil, fmt.Errorf("resolve image task history URLs: object key is outside the configured prefix")
		}
	}
	browser, err := h.factory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	resolver, ok := browser.(service.ImageStorageObjectURLResolver)
	if !ok {
		return nil, fmt.Errorf("resolve image task history URLs: storage browser cannot issue object URLs")
	}
	resolvedByKey, err := resolver.ResolveURLs(ctx, uniqueKeys)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(keysByTask))
	for taskID, keys := range keysByTask {
		for _, key := range keys {
			if resolved := strings.TrimSpace(resolvedByKey[key]); resolved != "" {
				result[taskID] = append(result[taskID], resolved)
			}
		}
	}
	return result, nil
}

func imageTaskDateRange(c *gin.Context) (int64, int64, error) {
	startRaw := strings.TrimSpace(c.Query("start_date"))
	endRaw := strings.TrimSpace(c.Query("end_date"))
	userTimezone := strings.TrimSpace(c.Query("timezone"))
	var startAt, endAt int64
	if startRaw != "" {
		start, err := appTimezone.ParseInUserLocation("2006-01-02", startRaw, userTimezone)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid start_date format, use YYYY-MM-DD")
		}
		startAt = start.Unix()
	}
	if endRaw != "" {
		end, err := appTimezone.ParseInUserLocation("2006-01-02", endRaw, userTimezone)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid end_date format, use YYYY-MM-DD")
		}
		endAt = end.AddDate(0, 0, 1).Unix()
	}
	if startAt > 0 && endAt > 0 && startAt >= endAt {
		return 0, 0, fmt.Errorf("start_date must be before or equal to end_date")
	}
	return startAt, endAt, nil
}

func imageGenerationTaskToView(task *service.ImageTaskRecord, now time.Time, freshResultURLs []string) imageGenerationTaskView {
	view := imageGenerationTaskView{
		ID:          task.ID,
		TaskID:      task.ID,
		UserID:      task.UserID,
		APIKeyID:    task.APIKeyID,
		Platform:    task.Platform,
		Operation:   task.Operation,
		Model:       task.Model,
		ImageCount:  task.ImageCount,
		Status:      task.Status,
		HTTPStatus:  task.HTTPStatus,
		CreatedAt:   task.CreatedAt,
		CompletedAt: task.CompletedAt,
		ExpiresAt:   task.ExpiresAt,
	}
	end := now
	if task.CompletedAt != nil {
		end = time.Unix(*task.CompletedAt, 0).UTC()
	}
	if created := time.Unix(task.CreatedAt, 0).UTC(); !created.IsZero() {
		view.DurationMs = end.Sub(created).Milliseconds()
		if view.DurationMs < 0 {
			view.DurationMs = 0
		}
	}
	if len(task.Error) > 0 {
		var taskError struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(task.Error, &taskError) == nil {
			view.ErrorType = strings.TrimSpace(taskError.Type)
			view.StopReason = strings.TrimSpace(taskError.Message)
		}
	}
	if len(task.ResultObjectKeys) > 0 {
		view.ResultCount = len(task.ResultObjectKeys)
		view.ResultURLs = append(view.ResultURLs, freshResultURLs...)
		return view
	}
	if len(task.Result) > 0 {
		var result struct {
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if json.Unmarshal(task.Result, &result) == nil {
			view.ResultCount = len(result.Data)
			for _, item := range result.Data {
				if url := strings.TrimSpace(item.URL); url != "" {
					view.ResultURLs = append(view.ResultURLs, url)
				}
			}
		}
	}
	return view
}

func intQuery(c *gin.Context, key string, fallback int) int {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
