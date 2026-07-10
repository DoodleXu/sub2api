package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type bulkResetUserSubRepoStub struct {
	mu sync.Mutex

	list       []service.UserSubscription
	dailyIDs   []int64
	weeklyIDs  []int64
	monthlyIDs []int64
}

func (r *bulkResetUserSubRepoStub) Create(context.Context, *service.UserSubscription) error {
	panic("unexpected Create call")
}
func (r *bulkResetUserSubRepoStub) GetByID(context.Context, int64) (*service.UserSubscription, error) {
	panic("unexpected GetByID call")
}
func (r *bulkResetUserSubRepoStub) GetByIDIncludeDeleted(context.Context, int64) (*service.UserSubscription, error) {
	panic("unexpected GetByIDIncludeDeleted call")
}
func (r *bulkResetUserSubRepoStub) GetByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	panic("unexpected GetByUserIDAndGroupID call")
}
func (r *bulkResetUserSubRepoStub) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*service.UserSubscription, error) {
	panic("unexpected GetActiveByUserIDAndGroupID call")
}
func (r *bulkResetUserSubRepoStub) Update(context.Context, *service.UserSubscription) error {
	panic("unexpected Update call")
}
func (r *bulkResetUserSubRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}
func (r *bulkResetUserSubRepoStub) Restore(context.Context, int64, string) (*service.UserSubscription, error) {
	panic("unexpected Restore call")
}
func (r *bulkResetUserSubRepoStub) ListByUserID(context.Context, int64) ([]service.UserSubscription, error) {
	panic("unexpected ListByUserID call")
}
func (r *bulkResetUserSubRepoStub) ListActiveByUserID(context.Context, int64) ([]service.UserSubscription, error) {
	panic("unexpected ListActiveByUserID call")
}
func (r *bulkResetUserSubRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}
func (r *bulkResetUserSubRepoStub) List(_ context.Context, params pagination.PaginationParams, _ *int64, _ *int64, status, _ string, _ string, _ string) ([]service.UserSubscription, *pagination.PaginationResult, error) {
	if status != service.SubscriptionStatusActive {
		return []service.UserSubscription{}, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	start := params.Offset()
	if start >= len(r.list) {
		return []service.UserSubscription{}, &pagination.PaginationResult{Total: int64(len(r.list)), Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
	}
	end := start + params.Limit()
	if end > len(r.list) {
		end = len(r.list)
	}
	pages := (len(r.list) + params.Limit() - 1) / params.Limit()
	if pages < 1 {
		pages = 1
	}
	out := append([]service.UserSubscription(nil), r.list[start:end]...)
	return out, &pagination.PaginationResult{Total: int64(len(r.list)), Page: params.Page, PageSize: params.PageSize, Pages: pages}, nil
}
func (r *bulkResetUserSubRepoStub) ExistsByUserIDAndGroupID(context.Context, int64, int64) (bool, error) {
	panic("unexpected ExistsByUserIDAndGroupID call")
}
func (r *bulkResetUserSubRepoStub) ExistsActiveByUserIDAndGroupID(context.Context, int64, int64) (bool, error) {
	panic("unexpected ExistsActiveByUserIDAndGroupID call")
}
func (r *bulkResetUserSubRepoStub) ExtendExpiry(context.Context, int64, time.Time) error {
	panic("unexpected ExtendExpiry call")
}
func (r *bulkResetUserSubRepoStub) UpdateStatus(context.Context, int64, string) error {
	panic("unexpected UpdateStatus call")
}
func (r *bulkResetUserSubRepoStub) UpdateNotes(context.Context, int64, string) error {
	panic("unexpected UpdateNotes call")
}
func (r *bulkResetUserSubRepoStub) ActivateWindows(context.Context, int64, time.Time) error {
	panic("unexpected ActivateWindows call")
}
func (r *bulkResetUserSubRepoStub) ResetUsageWindows(_ context.Context, id int64, resetDaily, resetWeekly, resetMonthly bool, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if resetDaily {
		r.dailyIDs = append(r.dailyIDs, id)
	}
	if resetWeekly {
		r.weeklyIDs = append(r.weeklyIDs, id)
	}
	if resetMonthly {
		r.monthlyIDs = append(r.monthlyIDs, id)
	}
	return nil
}
func (r *bulkResetUserSubRepoStub) ResetDailyUsage(_ context.Context, id int64, _ *time.Time, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dailyIDs = append(r.dailyIDs, id)
	return nil
}
func (r *bulkResetUserSubRepoStub) ResetWeeklyUsage(_ context.Context, id int64, _ *time.Time, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.weeklyIDs = append(r.weeklyIDs, id)
	return nil
}
func (r *bulkResetUserSubRepoStub) ResetMonthlyUsage(_ context.Context, id int64, _ *time.Time, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.monthlyIDs = append(r.monthlyIDs, id)
	return nil
}
func (r *bulkResetUserSubRepoStub) IncrementUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementUsage call")
}
func (r *bulkResetUserSubRepoStub) BatchUpdateExpiredStatus(context.Context) (int64, error) {
	panic("unexpected BatchUpdateExpiredStatus call")
}

func newBulkResetQuotaTestRouter(repo *bulkResetUserSubRepoStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := service.NewSubscriptionService(nil, repo, nil, nil, nil)
	h := NewSubscriptionHandler(svc)
	router := gin.New()
	router.POST("/admin/subscriptions/bulk-reset-quota/dry-run", h.BulkResetQuotaDryRun)
	router.POST("/admin/subscriptions/bulk-reset-quota", h.BulkResetQuota)
	return router
}

func postBulkResetQuota(router *gin.Engine, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/subscriptions/bulk-reset-quota", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func postBulkResetQuotaDryRun(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/subscriptions/bulk-reset-quota/dry-run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeBulkResetQuotaResponse(t *testing.T, rec *httptest.ResponseRecorder) service.BulkResetQuotaResult {
	t.Helper()
	var envelope struct {
		Code int                          `json:"code"`
		Data service.BulkResetQuotaResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Data
}

func TestSubscriptionHandlerBulkResetQuotaDryRun(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{
		list: []service.UserSubscription{
			{ID: 101, UserID: 1, GroupID: 10, Status: service.SubscriptionStatusActive},
			{ID: 102, UserID: 2, GroupID: 20, Status: service.SubscriptionStatusActive},
		},
	}
	router := newBulkResetQuotaTestRouter(repo)

	rec := postBulkResetQuotaDryRun(router, `{"daily":true,"weekly":true,"monthly":false}`)

	require.Equal(t, http.StatusOK, rec.Code)
	result := decodeBulkResetQuotaResponse(t, rec)
	require.True(t, result.DryRun)
	require.Equal(t, 2, result.AffectedCount)
	require.Equal(t, 0, result.Success)
	require.Empty(t, repo.dailyIDs)
	require.Empty(t, repo.weeklyIDs)
	require.Empty(t, repo.monthlyIDs)
}

func TestSubscriptionHandlerBulkResetQuotaRejectsMonthly(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{
		list: []service.UserSubscription{
			{ID: 101, UserID: 1, GroupID: 10, Status: service.SubscriptionStatusActive},
		},
	}
	router := newBulkResetQuotaTestRouter(repo)

	rec := postBulkResetQuota(router, `{"daily":true,"weekly":true,"monthly":true}`, "bulk-reset-monthly")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.dailyIDs)
	require.Empty(t, repo.weeklyIDs)
	require.Empty(t, repo.monthlyIDs)
}

func TestSubscriptionHandlerBulkResetQuotaRequiresIdempotencyKey(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{
		list: []service.UserSubscription{
			{ID: 101, UserID: 1, GroupID: 10, Status: service.SubscriptionStatusActive},
		},
	}
	router := newBulkResetQuotaTestRouter(repo)

	rec := postBulkResetQuota(router, `{"daily":true,"weekly":true,"monthly":false}`, "")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.dailyIDs)
	require.Empty(t, repo.weeklyIDs)
	require.Empty(t, repo.monthlyIDs)
}

func TestSubscriptionHandlerBulkResetQuotaIdempotentReplay(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{
		list: []service.UserSubscription{
			{ID: 101, UserID: 1, GroupID: 10, Status: service.SubscriptionStatusActive},
			{ID: 102, UserID: 2, GroupID: 20, Status: service.SubscriptionStatusActive},
		},
	}
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() {
		service.SetDefaultIdempotencyCoordinator(nil)
	})
	router := newBulkResetQuotaTestRouter(repo)
	body := `{"daily":true,"weekly":true,"monthly":false}`

	first := postBulkResetQuota(router, body, "same-bulk-reset-key")
	second := postBulkResetQuota(router, body, "same-bulk-reset-key")

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, []int64{101, 102}, repo.dailyIDs)
	require.Equal(t, []int64{101, 102}, repo.weeklyIDs)
	require.Empty(t, repo.monthlyIDs)

	firstResult := decodeBulkResetQuotaResponse(t, first)
	secondResult := decodeBulkResetQuotaResponse(t, second)
	require.Equal(t, firstResult.RunID, secondResult.RunID)
	require.Equal(t, []int64{101, 102}, firstResult.SuccessIDs)
	require.Equal(t, []int64{101, 102}, secondResult.SuccessIDs)
}
