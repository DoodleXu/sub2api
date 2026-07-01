//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// resetQuotaUserSubRepoStub 支持 GetByID、ResetDailyUsage、ResetWeeklyUsage、ResetMonthlyUsage，
// 其余方法继承 userSubRepoNoop（panic）。
type resetQuotaUserSubRepoStub struct {
	userSubRepoNoop

	sub            *UserSubscription
	list           []UserSubscription
	getActiveCalls int

	resetDailyCalled   bool
	resetWeeklyCalled  bool
	resetMonthlyCalled bool
	resetDailyIDs      []int64
	resetWeeklyIDs     []int64
	resetMonthlyIDs    []int64
	resetDailyErr      error
	resetWeeklyErr     error
	resetMonthlyErr    error
}

func (r *resetQuotaUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *resetQuotaUserSubRepoStub) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*UserSubscription, error) {
	r.getActiveCalls++
	if r.sub == nil || r.sub.UserID != userID || r.sub.GroupID != groupID || r.sub.Status != SubscriptionStatusActive || !r.sub.ExpiresAt.After(time.Now()) {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *resetQuotaUserSubRepoStub) List(_ context.Context, params pagination.PaginationParams, _ *int64, _ *int64, status, _ string, _ string, _ string) ([]UserSubscription, *pagination.PaginationResult, error) {
	if status != SubscriptionStatusActive {
		return nil, nil, nil
	}
	start := params.Offset()
	if start >= len(r.list) {
		return []UserSubscription{}, &pagination.PaginationResult{Total: int64(len(r.list)), Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
	}
	end := start + params.Limit()
	if end > len(r.list) {
		end = len(r.list)
	}
	out := append([]UserSubscription(nil), r.list[start:end]...)
	pages := (len(r.list) + params.Limit() - 1) / params.Limit()
	if pages < 1 {
		pages = 1
	}
	return out, &pagination.PaginationResult{Total: int64(len(r.list)), Page: params.Page, PageSize: params.PageSize, Pages: pages}, nil
}

func (r *resetQuotaUserSubRepoStub) ResetDailyUsage(_ context.Context, id int64, windowStart time.Time) error {
	r.resetDailyCalled = true
	r.resetDailyIDs = append(r.resetDailyIDs, id)
	if r.resetDailyErr == nil && r.sub != nil {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &windowStart
	}
	return r.resetDailyErr
}

func (r *resetQuotaUserSubRepoStub) ResetWeeklyUsage(_ context.Context, id int64, _ time.Time) error {
	r.resetWeeklyCalled = true
	r.resetWeeklyIDs = append(r.resetWeeklyIDs, id)
	return r.resetWeeklyErr
}

func (r *resetQuotaUserSubRepoStub) ResetMonthlyUsage(_ context.Context, id int64, _ time.Time) error {
	r.resetMonthlyCalled = true
	r.resetMonthlyIDs = append(r.resetMonthlyIDs, id)
	return r.resetMonthlyErr
}

func newResetQuotaSvc(stub *resetQuotaUserSubRepoStub) *SubscriptionService {
	return NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
}

func TestAdminResetQuota_ResetBoth(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 1, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 1, true, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_ResetDailyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 2, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 2, true, false, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_ResetWeeklyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 3, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 3, false, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_BothFalseReturnsError(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 7, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 7, false, false, false)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_SubscriptionNotFound(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{sub: nil}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 999, true, true, true)

	require.ErrorIs(t, err, ErrSubscriptionNotFound)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_ResetDailyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:           &UserSubscription{ID: 4, UserID: 10, GroupID: 20},
		resetDailyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 4, true, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled, "daily 失败后不应继续调用 weekly")
}

func TestAdminResetQuota_ResetWeeklyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:            &UserSubscription{ID: 5, UserID: 10, GroupID: 20},
		resetWeeklyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 5, false, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetWeeklyCalled)
}

func TestAdminResetQuota_ResetMonthlyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 8, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 8, false, false, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.True(t, stub.resetMonthlyCalled, "应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_ResetMonthlyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:             &UserSubscription{ID: 9, UserID: 10, GroupID: 20},
		resetMonthlyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 9, false, false, true)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_ReturnsRefreshedSub(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{
			ID:            6,
			UserID:        10,
			GroupID:       20,
			DailyUsageUSD: 99.9,
		},
	}

	svc := newResetQuotaSvc(stub)
	result, err := svc.AdminResetQuota(context.Background(), 6, true, false, false)

	require.NoError(t, err)
	// ResetDailyUsage stub 会将 sub.DailyUsageUSD 归零，
	// 服务应返回第二次 GetByID 的刷新值而非初始的 99.9
	require.Equal(t, float64(0), result.DailyUsageUSD, "返回的订阅应反映已归零的用量")
	require.True(t, stub.resetDailyCalled)
}

func TestAdminBulkResetQuota_ResetDailyAndWeeklyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		list: []UserSubscription{
			{ID: 11, UserID: 101, GroupID: 201, Status: SubscriptionStatusActive},
			{ID: 12, UserID: 102, GroupID: 202, Status: SubscriptionStatusActive},
		},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminBulkResetQuota(context.Background(), true, true, false)

	require.NoError(t, err)
	require.False(t, result.DryRun)
	require.NotEmpty(t, result.RunID)
	require.Equal(t, 2, result.AffectedCount)
	require.Equal(t, 2, result.Success)
	require.Equal(t, 0, result.Failed)
	require.Equal(t, []int64{11, 12}, result.SuccessIDs)
	require.Equal(t, []int64{11, 12}, stub.resetDailyIDs)
	require.Equal(t, []int64{11, 12}, stub.resetWeeklyIDs)
	require.Empty(t, stub.resetMonthlyIDs, "补偿型周配额重置不应清月用量")
}

func TestAdminBulkResetQuotaDryRun_CountsActiveSubscriptionsWithoutReset(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		list: []UserSubscription{
			{ID: 21, UserID: 201, GroupID: 301, Status: SubscriptionStatusActive},
			{ID: 22, UserID: 202, GroupID: 302, Status: SubscriptionStatusActive},
		},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminBulkResetQuotaDryRun(context.Background(), true, true, false)

	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Empty(t, result.RunID)
	require.Equal(t, 2, result.AffectedCount)
	require.Equal(t, 0, result.Success)
	require.Equal(t, 0, result.Failed)
	require.Empty(t, result.SuccessIDs)
	require.Empty(t, result.FailedIDs)
	require.Empty(t, stub.resetDailyIDs)
	require.Empty(t, stub.resetWeeklyIDs)
	require.Empty(t, stub.resetMonthlyIDs)
}

func TestAdminBulkResetQuota_AllFalseReturnsError(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminBulkResetQuota(context.Background(), false, false, false)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminBulkResetQuotaDryRun_RejectsMonthlyReset(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminBulkResetQuotaDryRun(context.Background(), true, true, true)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminBulkResetQuota_RejectsMonthlyReset(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminBulkResetQuota(context.Background(), true, true, true)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}
