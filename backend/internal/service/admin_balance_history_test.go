package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestMergeBalanceHistoryCodesIncludesAffiliateTransfersByDefault(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	older := now.Add(-2 * time.Hour)
	newer := now.Add(time.Hour)

	usedBy := int64(10)
	redeemCodes := []RedeemCode{
		{
			ID:        1,
			Type:      RedeemTypeBalance,
			Value:     8,
			Status:    StatusUsed,
			UsedBy:    &usedBy,
			UsedAt:    &now,
			CreatedAt: now,
		},
		{
			ID:        2,
			Type:      RedeemTypeConcurrency,
			Value:     1,
			Status:    StatusUsed,
			UsedBy:    &usedBy,
			UsedAt:    &older,
			CreatedAt: older,
		},
	}
	affiliateCodes := []RedeemCode{
		{
			ID:        -20,
			Type:      RedeemTypeAffiliateBalance,
			Value:     3.5,
			Status:    StatusUsed,
			UsedBy:    &usedBy,
			UsedAt:    &newer,
			CreatedAt: newer,
		},
	}

	got := mergeBalanceHistoryCodes(redeemCodes, affiliateCodes, pagination.PaginationParams{
		Page:     1,
		PageSize: 2,
	})

	require.Len(t, got, 2)
	require.Equal(t, RedeemTypeAffiliateBalance, got[0].Type)
	require.Equal(t, RedeemTypeBalance, got[1].Type)
}

func TestGetUserBalanceHistoryTotalsSeparatesCheckinRewards(t *testing.T) {
	t.Parallel()

	repo := &balanceHistoryTotalsRedeemRepo{
		recharged:     1000,
		checkinReward: 3,
	}
	svc := &adminServiceImpl{redeemCodeRepo: repo}

	totalRecharged, totalCheckinReward, err := svc.getUserBalanceHistoryTotals(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1000.0, totalRecharged)
	require.Equal(t, 3.0, totalCheckinReward)
	require.Equal(t, int64(1), repo.rechargedUserID)
	require.Equal(t, int64(1), repo.checkinUserID)
}

type balanceHistoryTotalsRedeemRepo struct {
	recharged       float64
	checkinReward   float64
	rechargedUserID int64
	checkinUserID   int64
}

func (r *balanceHistoryTotalsRedeemRepo) Create(context.Context, *RedeemCode) error {
	return errors.New("unexpected Create call")
}

func (r *balanceHistoryTotalsRedeemRepo) CreateBatch(context.Context, []RedeemCode) error {
	return errors.New("unexpected CreateBatch call")
}

func (r *balanceHistoryTotalsRedeemRepo) GetByID(context.Context, int64) (*RedeemCode, error) {
	return nil, errors.New("unexpected GetByID call")
}

func (r *balanceHistoryTotalsRedeemRepo) GetByCode(context.Context, string) (*RedeemCode, error) {
	return nil, errors.New("unexpected GetByCode call")
}

func (r *balanceHistoryTotalsRedeemRepo) Update(context.Context, *RedeemCode) error {
	return errors.New("unexpected Update call")
}

func (r *balanceHistoryTotalsRedeemRepo) BatchUpdate(context.Context, []int64, RedeemCodeBatchUpdateFields) (int64, error) {
	return 0, errors.New("unexpected BatchUpdate call")
}

func (r *balanceHistoryTotalsRedeemRepo) Delete(context.Context, int64) error {
	return errors.New("unexpected Delete call")
}

func (r *balanceHistoryTotalsRedeemRepo) Use(context.Context, int64, int64) error {
	return errors.New("unexpected Use call")
}

func (r *balanceHistoryTotalsRedeemRepo) List(context.Context, pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("unexpected List call")
}

func (r *balanceHistoryTotalsRedeemRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("unexpected ListWithFilters call")
}

func (r *balanceHistoryTotalsRedeemRepo) ListByUser(context.Context, int64, int) ([]RedeemCode, error) {
	return nil, errors.New("unexpected ListByUser call")
}

func (r *balanceHistoryTotalsRedeemRepo) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("unexpected ListByUserPaginated call")
}

func (r *balanceHistoryTotalsRedeemRepo) SumPositiveBalanceByUser(_ context.Context, userID int64) (float64, error) {
	r.rechargedUserID = userID
	return r.recharged, nil
}

func (r *balanceHistoryTotalsRedeemRepo) SumPositiveCheckinBalanceByUser(_ context.Context, userID int64) (float64, error) {
	r.checkinUserID = userID
	return r.checkinReward, nil
}

func TestMergeBalanceHistoryCodesPaginatesAfterCombiningSources(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	usedBy := int64(10)
	at := func(hours int) *time.Time {
		v := base.Add(time.Duration(hours) * time.Hour)
		return &v
	}

	got := mergeBalanceHistoryCodes(
		[]RedeemCode{
			{ID: 1, Type: RedeemTypeBalance, UsedBy: &usedBy, UsedAt: at(4), CreatedAt: *at(4)},
			{ID: 2, Type: RedeemTypeConcurrency, UsedBy: &usedBy, UsedAt: at(2), CreatedAt: *at(2)},
		},
		[]RedeemCode{
			{ID: -3, Type: RedeemTypeAffiliateBalance, UsedBy: &usedBy, UsedAt: at(3), CreatedAt: *at(3)},
			{ID: -4, Type: RedeemTypeAffiliateBalance, UsedBy: &usedBy, UsedAt: at(1), CreatedAt: *at(1)},
		},
		pagination.PaginationParams{Page: 2, PageSize: 2},
	)

	require.Len(t, got, 2)
	require.Equal(t, RedeemTypeConcurrency, got[0].Type)
	require.Equal(t, int64(-4), got[1].ID)
}
