package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ImageGenerationHandler struct {
	imageService *service.ImageGenerationArchiveService
}

func NewImageGenerationHandler(imageService *service.ImageGenerationArchiveService) *ImageGenerationHandler {
	return &ImageGenerationHandler{imageService: imageService}
}

func (h *ImageGenerationHandler) List(c *gin.Context) {
	params := service.ImageGenerationRecordListParams{
		Page:     intQuery(c, "page", 1),
		PageSize: intQuery(c, "page_size", 30),
		Model:    c.Query("model"),
		Status:   c.Query("status"),
		Source:   c.Query("source"),
	}
	if v := int64PtrQuery(c, "user_id"); v != nil {
		params.UserID = v
	}
	if v := int64PtrQuery(c, "api_key_id"); v != nil {
		params.APIKeyID = v
	}
	if v := parseTimeQuery(c.Query("start_date"), false); v != nil {
		params.StartAt = v
	}
	if v := parseTimeQuery(c.Query("end_date"), true); v != nil {
		params.EndAt = v
	}
	items, page, err := h.imageService.ListRecords(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		assets, _ := h.imageService.ListAssetsByRecordID(c.Request.Context(), item.ID)
		out = append(out, gin.H{
			"record": item,
			"assets": h.adminAssetResponses(assets),
		})
	}
	response.Success(c, gin.H{
		"items":     out,
		"total":     page.Total,
		"page":      page.Page,
		"page_size": page.Size,
		"pages":     page.Pages,
	})
}

func (h *ImageGenerationHandler) DailyStats(c *gin.Context) {
	now := time.Now()
	start := now.AddDate(0, 0, -29)
	end := now.AddDate(0, 0, 1)
	if v := parseTimeQuery(c.Query("start_date"), false); v != nil {
		start = *v
	}
	if v := parseTimeQuery(c.Query("end_date"), true); v != nil {
		end = *v
	}
	stats, err := h.imageService.ListDailyStats(c.Request.Context(), service.ImageGenerationRecordDailyStatsParams{
		StartDate: start,
		EndDate:   end,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": stats})
}

func (h *ImageGenerationHandler) StorageStats(c *gin.Context) {
	stats, err := h.imageService.GetStorageStats(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, stats)
}

func (h *ImageGenerationHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid record id")
		return
	}
	record, assets, err := h.imageService.GetRecordByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"record": record, "assets": h.adminAssetResponses(assets)})
}

func (h *ImageGenerationHandler) GetAsset(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("asset_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid asset id")
		return
	}
	asset, _, err := h.imageService.GetAssetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reader, err := h.imageService.OpenAsset(c.Request.Context(), asset)
	if err != nil {
		response.NotFound(c, "image asset is not available")
		return
	}
	writeAdminImageAssetReader(c, reader)
}

func (h *ImageGenerationHandler) GetStorageConfig(c *gin.Context) {
	cfg, err := h.imageService.GetStorageConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ImageGenerationHandler) UpdateStorageConfig(c *gin.Context) {
	var req service.ImageArchiveStorageConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	cfg, err := h.imageService.UpdateStorageConfig(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, cfg)
}

func (h *ImageGenerationHandler) adminAssetResponses(assets []*service.ImageGenerationAsset) []gin.H {
	out := make([]gin.H, 0, len(assets))
	for _, asset := range assets {
		adminURL := "/api/v1/admin/image-generations/assets/" + strconv.FormatInt(asset.ID, 10)
		rawURL := "/api/v1/image-assets/" + strconv.FormatInt(asset.ID, 10)
		url := rawURL
		if h != nil && h.imageService != nil {
			url = h.imageService.SignAssetURLPath(rawURL, asset.ID, service.ImageAssetScopeAdmin, time.Now().UTC())
		}
		out = append(out, gin.H{
			"id":          asset.ID,
			"record_id":   asset.RecordID,
			"asset_index": asset.AssetIndex,
			"mime_type":   asset.MimeType,
			"extension":   asset.Extension,
			"width":       asset.Width,
			"height":      asset.Height,
			"bytes":       asset.Bytes,
			"sha256":      asset.SHA256,
			"url":         url,
			"admin_url":   adminURL,
			"created_at":  asset.CreatedAt,
		})
	}
	return out
}

func writeAdminImageAssetReader(c *gin.Context, reader *service.ImageGenerationAssetReader) {
	if reader == nil || reader.Body == nil {
		response.NotFound(c, "image asset is not available")
		return
	}
	defer func() { _ = reader.Body.Close() }()
	c.DataFromReader(http.StatusOK, reader.Size, reader.ContentType, reader.Body, map[string]string{
		"Content-Disposition": "inline; filename=\"" + reader.Filename + "\"",
		"Cache-Control":       "private, max-age=86400",
	})
}

func intQuery(c *gin.Context, key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func int64PtrQuery(c *gin.Context, key string) *int64 {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func parseTimeQuery(raw string, endOfDay bool) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if endOfDay {
			t = t.AddDate(0, 0, 1)
		}
		return &t
	}
	return nil
}
