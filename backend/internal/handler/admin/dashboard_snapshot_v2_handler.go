package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)

type dashboardSnapshotV2Stats struct {
	usagestats.DashboardStats
	Uptime int64 `json:"uptime"`
}

type dashboardSnapshotV2Response struct {
	GeneratedAt string `json:"generated_at"`

	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Granularity string `json:"granularity"`

	Stats      *dashboardSnapshotV2Stats        `json:"stats,omitempty"`
	Trend      []usagestats.TrendDataPoint      `json:"trend,omitempty"`
	Models     []usagestats.ModelStat           `json:"models,omitempty"`
	Groups     []usagestats.GroupStat           `json:"groups,omitempty"`
	UsersTrend []usagestats.UserUsageTrendPoint `json:"users_trend,omitempty"`

	Ranking                []usagestats.UserSpendingRankingItem `json:"ranking,omitempty"`
	RankingTotalActualCost float64                              `json:"ranking_total_actual_cost,omitempty"`
	RankingTotalRequests   int64                                `json:"ranking_total_requests,omitempty"`
	RankingTotalTokens     int64                                `json:"ranking_total_tokens,omitempty"`
}

type dashboardSnapshotV2Filters struct {
	UserID      int64
	APIKeyID    int64
	AccountID   int64
	GroupID     int64
	Model       string
	RequestType *int16
	Stream      *bool
	BillingType *int8
}

type dashboardSnapshotV2CacheKey struct {
	StartTime         string `json:"start_time"`
	EndTime           string `json:"end_time"`
	Granularity       string `json:"granularity"`
	UserID            int64  `json:"user_id"`
	APIKeyID          int64  `json:"api_key_id"`
	AccountID         int64  `json:"account_id"`
	GroupID           int64  `json:"group_id"`
	Model             string `json:"model"`
	RequestType       *int16 `json:"request_type"`
	Stream            *bool  `json:"stream"`
	BillingType       *int8  `json:"billing_type"`
	IncludeStats      bool   `json:"include_stats"`
	IncludeTrend      bool   `json:"include_trend"`
	IncludeModels     bool   `json:"include_models"`
	IncludeGroups     bool   `json:"include_groups"`
	IncludeUsersTrend bool   `json:"include_users_trend"`
	IncludeRanking    bool   `json:"include_ranking"`
	UsersTrendLimit   int    `json:"users_trend_limit"`
	RankingLimit      int    `json:"ranking_limit"`
}

func (h *DashboardHandler) GetSnapshotV2(c *gin.Context) {
	startTime, endTime := parseTimeRange(c)
	granularity := strings.TrimSpace(c.DefaultQuery("granularity", "day"))
	if granularity != "hour" {
		granularity = "day"
	}

	includeStats := parseBoolQueryWithDefault(c.Query("include_stats"), true)
	includeTrend := parseBoolQueryWithDefault(c.Query("include_trend"), true)
	includeModels := parseBoolQueryWithDefault(c.Query("include_model_stats"), true)
	includeGroups := parseBoolQueryWithDefault(c.Query("include_group_stats"), false)
	includeUsersTrend := parseBoolQueryWithDefault(c.Query("include_users_trend"), false)
	includeRanking := parseBoolQueryWithDefault(c.Query("include_user_ranking"), false)
	usersTrendLimit := 12
	if raw := strings.TrimSpace(c.Query("users_trend_limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 50 {
			usersTrendLimit = parsed
		}
	}
	rankingLimit := 12
	if raw := strings.TrimSpace(c.Query("user_ranking_limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 50 {
			rankingLimit = parsed
		}
	}

	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	keyRaw, _ := json.Marshal(dashboardSnapshotV2CacheKey{
		StartTime:         startTime.UTC().Format(time.RFC3339),
		EndTime:           endTime.UTC().Format(time.RFC3339),
		Granularity:       granularity,
		UserID:            filters.UserID,
		APIKeyID:          filters.APIKeyID,
		AccountID:         filters.AccountID,
		GroupID:           filters.GroupID,
		Model:             filters.Model,
		RequestType:       filters.RequestType,
		Stream:            filters.Stream,
		BillingType:       filters.BillingType,
		IncludeStats:      includeStats,
		IncludeTrend:      includeTrend,
		IncludeModels:     includeModels,
		IncludeGroups:     includeGroups,
		IncludeUsersTrend: includeUsersTrend,
		IncludeRanking:    includeRanking,
		UsersTrendLimit:   usersTrendLimit,
		RankingLimit:      rankingLimit,
	})
	cacheKey := string(keyRaw)

	cached, hit, err := dashboardSnapshotV2Cache.GetOrLoad(cacheKey, func() (any, error) {
		return h.buildSnapshotV2Response(
			c.Request.Context(),
			startTime,
			endTime,
			granularity,
			filters,
			includeStats,
			includeTrend,
			includeModels,
			includeGroups,
			includeUsersTrend,
			includeRanking,
			usersTrendLimit,
			rankingLimit,
		)
	})
	if err != nil {
		response.Error(c, 500, err.Error())
		return
	}
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
		if ifNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
			c.Status(http.StatusNotModified)
			return
		}
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, cached.Payload)
}

func (h *DashboardHandler) buildSnapshotV2Response(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	filters *dashboardSnapshotV2Filters,
	includeStats, includeTrend, includeModels, includeGroups, includeUsersTrend, includeRanking bool,
	usersTrendLimit, rankingLimit int,
) (*dashboardSnapshotV2Response, error) {
	resp := &dashboardSnapshotV2Response{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		StartDate:   startTime.Format("2006-01-02"),
		EndDate:     endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		Granularity: granularity,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	errCh := make(chan error, 6)
	run := func(load func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := load(); err != nil {
				errCh <- err
			}
		}()
	}

	if includeStats {
		run(func() error {
			stats, err := h.dashboardService.GetDashboardStats(ctx)
			if err != nil {
				return errors.New("failed to get dashboard statistics")
			}
			mu.Lock()
			resp.Stats = &dashboardSnapshotV2Stats{
				DashboardStats: *stats,
				Uptime:         int64(time.Since(h.startTime).Seconds()),
			}
			mu.Unlock()
			return nil
		})
	}

	if includeTrend {
		run(func() error {
			trend, _, err := h.getUsageTrendCached(
				ctx,
				startTime,
				endTime,
				granularity,
				filters.UserID,
				filters.APIKeyID,
				filters.AccountID,
				filters.GroupID,
				filters.Model,
				filters.RequestType,
				filters.Stream,
				filters.BillingType,
			)
			if err != nil {
				return errors.New("failed to get usage trend")
			}
			mu.Lock()
			resp.Trend = trend
			mu.Unlock()
			return nil
		})
	}

	if includeModels {
		run(func() error {
			models, _, err := h.getModelStatsCached(
				ctx,
				startTime,
				endTime,
				filters.UserID,
				filters.APIKeyID,
				filters.AccountID,
				filters.GroupID,
				usagestats.ModelSourceRequested,
				filters.RequestType,
				filters.Stream,
				filters.BillingType,
			)
			if err != nil {
				return errors.New("failed to get model statistics")
			}
			mu.Lock()
			resp.Models = models
			mu.Unlock()
			return nil
		})
	}

	if includeGroups {
		run(func() error {
			groups, _, err := h.getGroupStatsCached(
				ctx,
				startTime,
				endTime,
				filters.UserID,
				filters.APIKeyID,
				filters.AccountID,
				filters.GroupID,
				filters.RequestType,
				filters.Stream,
				filters.BillingType,
			)
			if err != nil {
				return errors.New("failed to get group statistics")
			}
			mu.Lock()
			resp.Groups = groups
			mu.Unlock()
			return nil
		})
	}

	if includeUsersTrend {
		run(func() error {
			usersTrend, _, err := h.getUserUsageTrendCached(ctx, startTime, endTime, granularity, usersTrendLimit)
			if err != nil {
				return errors.New("failed to get user usage trend")
			}
			mu.Lock()
			resp.UsersTrend = usersTrend
			mu.Unlock()
			return nil
		})
	}

	if includeRanking {
		run(func() error {
			ranking, err := h.dashboardService.GetUserSpendingRanking(ctx, startTime, endTime, rankingLimit)
			if err != nil {
				return errors.New("failed to get user spending ranking")
			}
			mu.Lock()
			resp.Ranking = ranking.Ranking
			resp.RankingTotalActualCost = ranking.TotalActualCost
			resp.RankingTotalRequests = ranking.TotalRequests
			resp.RankingTotalTokens = ranking.TotalTokens
			mu.Unlock()
			return nil
		})
	}

	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, err
	}

	return resp, nil
}

func parseDashboardSnapshotV2Filters(c *gin.Context) (*dashboardSnapshotV2Filters, error) {
	filters := &dashboardSnapshotV2Filters{
		Model: strings.TrimSpace(c.Query("model")),
	}

	if userIDStr := strings.TrimSpace(c.Query("user_id")); userIDStr != "" {
		id, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.UserID = id
	}
	if apiKeyIDStr := strings.TrimSpace(c.Query("api_key_id")); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.APIKeyID = id
	}
	if accountIDStr := strings.TrimSpace(c.Query("account_id")); accountIDStr != "" {
		id, err := strconv.ParseInt(accountIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.AccountID = id
	}
	if groupIDStr := strings.TrimSpace(c.Query("group_id")); groupIDStr != "" {
		id, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.GroupID = id
	}

	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := service.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			return nil, err
		}
		value := int16(parsed)
		filters.RequestType = &value
	} else if streamStr := strings.TrimSpace(c.Query("stream")); streamStr != "" {
		streamVal, err := strconv.ParseBool(streamStr)
		if err != nil {
			return nil, err
		}
		filters.Stream = &streamVal
	}

	if billingTypeStr := strings.TrimSpace(c.Query("billing_type")); billingTypeStr != "" {
		v, err := strconv.ParseInt(billingTypeStr, 10, 8)
		if err != nil {
			return nil, err
		}
		bt := int8(v)
		filters.BillingType = &bt
	}

	return filters, nil
}
