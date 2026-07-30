package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type limitContinuationRateLimitRepo struct {
	AccountRepository
	rateLimitCalls int
	tempCalls      int
}

func (r *limitContinuationRateLimitRepo) SetRateLimited(_ context.Context, _ int64, _ time.Time) error {
	r.rateLimitCalls++
	return nil
}

func (r *limitContinuationRateLimitRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.tempCalls++
	return nil
}

func limitContinuationAccount(id int64, used5h float64) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Extra: map[string]any{
			OpenAIContinueSchedulingAfterLimitExtraKey: true,
			"codex_5h_used_percent":                    used5h,
			"codex_5h_reset_at":                        time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339),
			"codex_usage_updated_at":                   time.Now().UTC().Format(time.RFC3339),
		},
	}
}

func TestOpenAILimitContinuation_AccountLevelRateLimitRemainsSchedulable(t *testing.T) {
	resetAt := time.Now().Add(time.Hour)
	account := limitContinuationAccount(71001, 100)
	account.RateLimitResetAt = &resetAt

	require.True(t, account.IsRateLimited())
	require.True(t, account.IsSchedulable())

	account.Extra[OpenAIContinueSchedulingAfterLimitExtraKey] = false
	require.False(t, account.IsSchedulable())
}

func TestOpenAILimitContinuation_LocalAPIKeyQuotaRemainsSchedulable(t *testing.T) {
	account := limitContinuationAccount(71002, 100)
	account.Type = AccountTypeAPIKey
	account.Extra["quota_limit"] = 10.0
	account.Extra["quota_used"] = 10.0
	account.Extra["quota_daily_limit"] = 5.0
	account.Extra["quota_daily_used"] = 5.0
	account.Extra["quota_daily_start"] = time.Now().Add(-time.Hour).Format(time.RFC3339)

	require.True(t, account.IsQuotaExceeded())
	require.True(t, account.IsSchedulable())
}

func TestOpenAILimitContinuation_Persisted429TempBlockRemainsSchedulable(t *testing.T) {
	account := limitContinuationAccount(71004, 100)
	until := time.Now().Add(30 * time.Minute)
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = `{"status_code":429,"error_message":"rate limited"}`
	require.True(t, account.IsSchedulable())

	account.TempUnschedulableReason = `{"status_code":401,"error_message":"unauthorized"}`
	require.False(t, account.IsSchedulable())
}

func TestOpenAILimitContinuation_429DoesNotCreateCustomTempUnschedulableBlock(t *testing.T) {
	account := limitContinuationAccount(71003, 100)
	account.Type = AccountTypeAPIKey
	account.Credentials = map[string]any{
		"api_key": "sk-test",
		"temp_unschedulable_rules": []any{map[string]any{
			"error_code":       float64(http.StatusTooManyRequests),
			"keywords":         []any{"rate limited"},
			"duration_minutes": float64(30),
		}},
	}
	repo := &limitContinuationRateLimitRepo{}
	svc := &RateLimitService{accountRepo: repo, cfg: &config.Config{}}

	handled := svc.HandleUpstreamError(
		context.Background(),
		&account,
		http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"60"}},
		[]byte(`{"error":{"message":"rate limited"}}`),
		"gpt-5.4",
	)

	require.False(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Equal(t, 1, repo.rateLimitCalls)
}

func TestOpenAILimitContinuation_AllRequestsPrioritizeExhaustedAccount(t *testing.T) {
	primary := limitContinuationAccount(71011, 100)
	secondary := Account{
		ID: 71012, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
	}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}},
		cfg:         &config.Config{},
	}

	selected, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, primary.ID, selected.ID)

	selected, err = svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.1", map[int64]struct{}{primary.ID: {}})
	require.NoError(t, err)
	require.Equal(t, secondary.ID, selected.ID)
}

func TestOpenAILimitContinuation_AllRequestsPrioritizeExhausted7dAccount(t *testing.T) {
	primary := limitContinuationAccount(71013, 20)
	primary.Extra["codex_7d_used_percent"] = 100.0
	primary.Extra["codex_7d_reset_at"] = time.Now().Add(6 * 24 * time.Hour).UTC().Format(time.RFC3339)
	secondary := Account{
		ID: 71014, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
	}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}},
		cfg:         &config.Config{},
	}

	selected, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, primary.ID, selected.ID)
}

func TestOpenAILimitContinuation_SchedulerPrioritizesTierThenFallsBackToNormalAccount(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		advanced := advanced
		t.Run(fmt.Sprintf("advanced=%t", advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			listCalls := 0
			overclock := limitContinuationAccount(71015, 100)
			overclock.Priority = 100
			normal := Account{
				ID: 71016, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			}
			cache := &schedulerTestGatewayCache{
				sessionBindings: map[string]int64{"openai:existing": normal.ID},
			}
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{overclock, normal}, listCalls: &listCalls},
				cache:       cache,
				cfg:         &config.Config{},
			}
			if advanced {
				svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
			}

			selection, _, err := svc.SelectAccountWithScheduler(
				context.Background(), nil, "", "existing", "gpt-5.1", nil,
				OpenAIUpstreamTransportAny, false,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, overclock.ID, selection.Account.ID)
			require.Equal(t, 1, listCalls)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}

			selection, _, err = svc.SelectAccountWithScheduler(
				context.Background(), nil, "", "existing", "gpt-5.1",
				map[int64]struct{}{overclock.ID: {}}, OpenAIUpstreamTransportAny, false,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, normal.ID, selection.Account.ID)
			require.Equal(t, 2, listCalls)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAILimitContinuation_SchedulerWithoutPriorityTierListsAccountsOnce(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		advanced := advanced
		t.Run(fmt.Sprintf("advanced=%t", advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			listCalls := 0
			normal := Account{
				ID: 71017, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
			}
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{normal}, listCalls: &listCalls},
				cfg:         &config.Config{},
			}
			if advanced {
				svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
			}

			selection, _, err := svc.SelectAccountWithScheduler(
				context.Background(), nil, "", "", "gpt-5.1", nil,
				OpenAIUpstreamTransportAny, false,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, normal.ID, selection.Account.ID)
			require.Equal(t, 1, listCalls)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestOpenAILimitContinuation_AllRequestsAllowAccountBelowRealLimit(t *testing.T) {
	primary := limitContinuationAccount(71021, 99)
	primary.Extra["auto_pause_5h_threshold"] = 0.95
	secondary := Account{
		ID: 71022, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
	}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{primary, secondary}},
		cfg:         &config.Config{},
	}
	svc.BlockAccountScheduling(&primary, time.Now().Add(time.Hour), "429")

	selected, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.Equal(t, primary.ID, selected.ID)
}

func TestOpenAILimitContinuation_BypassesOnlyRateLimitRuntimeBlock(t *testing.T) {
	account := limitContinuationAccount(71031, 100)
	svc := &OpenAIGatewayService{}

	svc.BlockAccountScheduling(&account, time.Now().Add(time.Hour), "429")
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))

	svc.BlockAccountScheduling(&account, time.Now().Add(2*time.Hour), "upstream_disable")
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))
}

func TestOpenAILimitContinuation_ShorterNonLimitBlockDoesNotRelabelLonger429Block(t *testing.T) {
	account := limitContinuationAccount(71032, 100)
	svc := &OpenAIGatewayService{}
	longUntil := time.Now().Add(4 * time.Hour)

	svc.BlockAccountScheduling(&account, longUntil, "429")
	svc.BlockAccountScheduling(&account, time.Now().Add(10*time.Minute), "transport_error")

	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))
	reason, ok := svc.openaiAccountRuntimeBlockReason.Load(account.ID)
	require.True(t, ok)
	require.Equal(t, "429", reason)

	svc.openaiAccountRuntimeHardBlockUntil.Store(account.ID, time.Now().Add(-time.Second))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))
}

func TestOpenAILimitContinuation_Longer429OutlivesEarlierNonLimitBlock(t *testing.T) {
	account := limitContinuationAccount(71033, 100)
	svc := &OpenAIGatewayService{}

	svc.BlockAccountScheduling(&account, time.Now().Add(10*time.Minute), "transport_error")
	svc.BlockAccountScheduling(&account, time.Now().Add(4*time.Hour), "429")
	require.True(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))

	svc.openaiAccountRuntimeHardBlockUntil.Store(account.ID, time.Now().Add(-time.Second))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlockedWithContext(context.Background(), &account, "gpt-5.1"))
}

func TestRuntimeHardBlock_GrokCredentialRollbackRestoresEarlierLimitState(t *testing.T) {
	account := Account{ID: 71034, Platform: PlatformGrok, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	limitUntil := time.Now().Add(4 * time.Hour)

	svc.BlockAccountScheduling(&account, limitUntil, "429")
	restore := svc.blockGrokCredentialRuntime(&account, time.Now().Add(10*time.Minute), "credential_refresh")
	require.True(t, svc.isOpenAIAccountRuntimeHardBlocked(account.ID))

	restore()
	require.False(t, svc.isOpenAIAccountRuntimeHardBlocked(account.ID))
	reason, ok := svc.openaiAccountRuntimeBlockReason.Load(account.ID)
	require.True(t, ok)
	require.Equal(t, "429", reason)
	blockedUntil, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	require.Equal(t, limitUntil, blockedUntil)
}

func TestPrepareOpenAILimitContinuationFailover(t *testing.T) {
	account := limitContinuationAccount(71041, 100)
	failoverErr := &UpstreamFailoverError{
		StatusCode:             http.StatusTooManyRequests,
		NextAccountAction:      NextAccountStop,
		RetryableOnSameAccount: true,
		ResponseBody:           []byte(`{"error":{"message":"private upstream detail"}}`),
	}

	require.True(t, PrepareOpenAILimitContinuationFailover(&account, failoverErr))
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.SuppressClientError)

	requestErr := &UpstreamFailoverError{StatusCode: http.StatusBadRequest, Scope: GatewayFailureScopeRequest, NextAccountAction: NextAccountStop}
	require.False(t, PrepareOpenAILimitContinuationFailover(&account, requestErr))
	require.False(t, requestErr.SuppressClientError)
}
