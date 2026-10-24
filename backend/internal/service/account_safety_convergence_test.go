package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSchedulingSafetyDoesNotSerializeColdAccounts(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			s := NewRateLimitService(nil, nil, nil, nil, nil)
			account := &Account{ID: 1, Platform: platform, Concurrency: 100}
			for i := 0; i < 32; i++ {
				ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "new-model", Stream: true})
				ctx, attempt, err := s.BeginAccountQualityAttempt(ctx, account)
				require.NoError(t, err, "unknown history must not reduce configured concurrency")
				require.NotNil(t, attempt)
				defer attempt.Abort()
				require.NoError(t, ValidateAccountQualityDispatch(ctx))
			}
		})
	}
}

type safetyProjectionRepo struct {
	*balanceSafetyRepo
	fullReads   int
	safetyReads int
}

func (r *safetyProjectionRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.fullReads++
	return r.balanceSafetyRepo.GetByID(ctx, id)
}
func (r *safetyProjectionRepo) GetAccountDispatchState(ctx context.Context, id int64) (*Account, error) {
	r.safetyReads++
	return r.balanceSafetyRepo.GetByID(ctx, id)
}

func TestDispatchSafetyUsesProjectionAndStillFencesAdminChanges(t *testing.T) {
	r := &safetyProjectionRepo{balanceSafetyRepo: &balanceSafetyRepo{current: Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Extra: map[string]any{}}}}
	s := NewRateLimitService(r, nil, nil, nil, nil)
	ctx, attempt, err := s.BeginAccountQualityAttempt(context.Background(), &r.current)
	require.NoError(t, err)
	defer attempt.Abort()
	require.NoError(t, ValidateAccountQualityDispatch(ctx))
	require.Zero(t, r.fullReads, "safety checks must not hydrate account groups/proxies")
	require.Equal(t, 2, r.safetyReads, "both adapter and actual outbound checks stay authoritative")
	r.current.Schedulable = false
	require.Error(t, ValidateAccountQualityDispatch(ctx), "an admin pause between begin and outbound must still block")
}

func TestSchedulingSafetyKeepsProtocolRepairBudgetAndFinancialFence(t *testing.T) {
	s := NewRateLimitService(nil, nil, nil, nil, nil)
	ctx, a, err := s.BeginAccountQualityAttempt(context.Background(), &Account{ID: 1})
	require.NoError(t, err)
	defer a.Abort()
	require.True(t, AllowAccountProtocolRepair(ctx))
	require.False(t, AllowAccountProtocolRepair(ctx))
	a.balance.Store(true)
	require.False(t, AllowAccountProtocolRepair(ctx))
	var failure *UpstreamFailoverError
	require.ErrorAs(t, a.Finish(nil, nil), &failure)
	require.Equal(t, GatewayFailureReason("insufficient_account_balance"), failure.Reason)
	require.False(t, failure.RetryableOnSameAccount)
}
