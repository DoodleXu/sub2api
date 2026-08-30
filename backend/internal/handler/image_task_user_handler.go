package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// imageTaskUserView is intentionally separate from service.ImageTaskRecord:
// ownership fields and durable object keys must never cross this boundary.
// Durable tasks omit the completion payload (which may contain an expired
// presigned URL) and expose only URLs freshly resolved by the service.
type imageTaskUserView struct {
	ID              string          `json:"id"`
	TaskID          string          `json:"task_id"`
	Object          string          `json:"object"`
	Status          string          `json:"status"`
	HTTPStatus      int             `json:"http_status,omitempty"`
	Platform        string          `json:"platform,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Model           string          `json:"model,omitempty"`
	ImageCount      int             `json:"image_count,omitempty"`
	ResultCount     int             `json:"result_count,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	ResultURLs      []string        `json:"result_urls,omitempty"`
	Error           json.RawMessage `json:"error,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	CompletedAt     *int64          `json:"completed_at,omitempty"`
	ExpiresAt       int64           `json:"expires_at"`
	AssetsAvailable bool            `json:"assets_available"`
	AssetsExpired   bool            `json:"assets_expired,omitempty"`
}

func (h *AsyncImageHandler) ListUser(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not found in context")
		return
	}
	if !h.pollable() {
		response.NotFound(c, "async image tasks are not enabled")
		return
	}
	query, err := parseImageTaskUserQuery(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	page, err := h.tasks.ListUser(c.Request.Context(), query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]imageTaskUserView, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		items = append(items, h.imageTaskUserView(c, task))
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{
		"items":       items,
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
		"server_time": nowUnixMilli(),
	})
}

func (h *AsyncImageHandler) GetUser(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not found in context")
		return
	}
	if !h.pollable() {
		response.NotFound(c, "async image tasks are not enabled")
		return
	}
	id, err := validateImageTaskID(c.Param("task_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	task, err := h.tasks.GetUser(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, h.imageTaskUserView(c, task))
}

// DeleteUser removes one terminal task from the authenticated user's history
// and its durable image objects. Processing tasks are intentionally rejected
// so a detached completion cannot recreate a record after deletion.
func (h *AsyncImageHandler) DeleteUser(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not found in context")
		return
	}
	if !h.pollable() {
		response.NotFound(c, "async image tasks are not enabled")
		return
	}
	id, err := validateImageTaskID(c.Param("task_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.tasks.DeleteUser(c.Request.Context(), subject.UserID, id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

// GetUserAsset returns a single fresh URL. It never redirects automatically:
// the desktop client can inspect the short-lived grant and download it with a
// normal HTTPS client, while browser referrers remain on the API origin.
func (h *AsyncImageHandler) GetUserAsset(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not found in context")
		return
	}
	if !h.pollable() {
		response.NotFound(c, "async image tasks are not enabled")
		return
	}
	id, err := validateImageTaskID(c.Param("task_id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	index, parseErr := strconv.Atoi(strings.TrimSpace(c.Param("index")))
	if parseErr != nil || index < 0 || index > 99 {
		response.BadRequest(c, "invalid image asset index")
		return
	}
	task, err := h.tasks.GetUser(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	urls, err := h.tasks.ResolveResultURLs(c.Request.Context(), task)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if index >= len(urls) || strings.TrimSpace(urls[index]) == "" {
		response.NotFound(c, "image asset not found")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{
		"task_id":     task.ID,
		"asset_index": index,
		"url":         urls[index],
		"expires_at":  task.ExpiresAt,
	})
}

func parseImageTaskUserQuery(c *gin.Context, userID int64) (service.ImageTaskUserQuery, error) {
	query := service.ImageTaskUserQuery{
		UserID: userID,
		Status: strings.TrimSpace(c.Query("status")),
		Cursor: strings.TrimSpace(c.Query("cursor")),
		Limit:  50,
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return service.ImageTaskUserQuery{}, badImageTaskQuery("limit must be a positive integer")
		}
		query.Limit = limit
	}
	for key, target := range map[string]*int64{"start_at": &query.StartAt, "end_at": &query.EndAt} {
		raw := strings.TrimSpace(c.Query(key))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return service.ImageTaskUserQuery{}, badImageTaskQuery(fmt.Sprintf("%s must be a non-negative Unix timestamp", key))
		}
		*target = value
	}
	return query, nil
}

func validateImageTaskID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" || len(id) > 96 {
		return "", badImageTaskQuery("invalid image task id")
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' && ch != '-' {
			return "", badImageTaskQuery("invalid image task id")
		}
	}
	return id, nil
}

func badImageTaskQuery(message string) error {
	return infraerrors.BadRequest("INVALID_IMAGE_TASK_QUERY", message)
}

func nowUnixMilli() int64 {
	return time.Now().UnixMilli()
}

func (h *AsyncImageHandler) imageTaskUserView(c *gin.Context, task *service.ImageTaskRecord) imageTaskUserView {
	view := imageTaskUserView{
		AssetsAvailable: false,
	}
	if task == nil {
		return view
	}
	view.ID = task.ID
	view.TaskID = task.ID
	view.Object = "image.generation.task"
	view.Status = task.Status
	view.HTTPStatus = task.HTTPStatus
	view.Platform = task.Platform
	view.Operation = task.Operation
	view.Model = task.Model
	view.ImageCount = task.ImageCount
	view.Error = safeImageTaskError(task.Error)
	view.CreatedAt = task.CreatedAt
	view.CompletedAt = task.CompletedAt
	view.ExpiresAt = task.ExpiresAt
	if len(task.ResultObjectKeys) == 0 {
		view.ResultURLs = service.ImageTaskResultURLs(task.Result)
		view.ResultCount = len(view.ResultURLs)
		// Legacy records may contain an inline b64_json payload (written before
		// object storage became mandatory). Never copy that private execution
		// blob into a user-facing response; retain only the already-public HTTPS
		// links in a compact, safe result envelope.
		view.Result = safeLegacyImageTaskResult(view.ResultURLs)
		// ExpiresAt is a runtime lease only while the task is processing. A
		// terminal task's history remains available until the user deletes it;
		// object-store lifecycle policy is the separate physical-retention
		// backstop.
		processingExpired := task.Status == service.ImageTaskStatusProcessing && task.ExpiresAt > 0 && time.Now().Unix() >= task.ExpiresAt
		view.AssetsAvailable = len(view.ResultURLs) > 0 && !processingExpired
		view.AssetsExpired = len(view.ResultURLs) > 0 && processingExpired
		return view
	}
	urls, err := h.tasks.ResolveResultURLs(c.Request.Context(), task)
	if err == nil {
		view.ResultURLs = urls
		view.ResultCount = len(urls)
		view.AssetsAvailable = len(urls) > 0
	} else if errors.Is(err, service.ErrImageTaskAssetsExpired) {
		view.AssetsExpired = true
	}
	return view
}

func safeLegacyImageTaskResult(urls []string) json.RawMessage {
	if len(urls) == 0 {
		return nil
	}
	data := make([]map[string]string, 0, len(urls))
	for _, rawURL := range urls {
		if value := strings.TrimSpace(rawURL); value != "" {
			data = append(data, map[string]string{"url": value})
		}
	}
	if len(data) == 0 {
		return nil
	}
	encoded, err := json.Marshal(struct {
		Data []map[string]string `json:"data"`
	}{Data: data})
	if err != nil {
		return nil
	}
	return encoded
}

func safeImageTaskError(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value struct {
		Type    string `json:"type,omitempty"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	value.Type = truncateImageTaskErrorField(value.Type)
	value.Code = truncateImageTaskErrorField(value.Code)
	value.Message = truncateImageTaskErrorField(value.Message)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func truncateImageTaskErrorField(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
