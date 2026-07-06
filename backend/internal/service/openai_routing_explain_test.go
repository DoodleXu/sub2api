package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type openAIRoutingExplainTestRepo struct {
	AccountRepository
	accounts []Account
}

func (r openAIRoutingExplainTestRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, *pagination.PaginationResult, error) {
	return r.accounts, &pagination.PaginationResult{Total: int64(len(r.accounts)), Page: 1, PageSize: len(r.accounts)}, nil
}

func (r openAIRoutingExplainTestRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	result := make([]*Account, 0, len(ids))
	for i := range r.accounts {
		if _, ok := seen[r.accounts[i].ID]; ok {
			result = append(result, &r.accounts[i])
		}
	}
	return result, nil
}

func (r openAIRoutingExplainTestRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, nil
}

func setOpenAIRoutingExplainStrategyForTest(t *testing.T, strategy string) {
	t.Helper()
	previous, hadPrevious := openAIAccountSchedulerStrategySettingCache.Load().(*cachedOpenAIAccountSchedulerStrategySetting)
	openAIAccountSchedulerStrategySettingCache.Store(&cachedOpenAIAccountSchedulerStrategySetting{
		strategy:                    NormalizeOpenAIAccountSchedulerStrategy(strategy),
		experimentalRetry:           DefaultOpenAIAccountExperimentalRetryCount,
		experimentalRecordRecovered: false,
		strictRetry:                 DefaultOpenAIAccountStrictRetryCount,
		strictRecordRecovered:       false,
		expiresAt:                   time.Now().Add(time.Hour).UnixNano(),
	})
	t.Cleanup(func() {
		if hadPrevious && previous != nil {
			openAIAccountSchedulerStrategySettingCache.Store(previous)
			return
		}
		openAIAccountSchedulerStrategySettingCache.Store(&cachedOpenAIAccountSchedulerStrategySetting{
			strategy:  OpenAIAccountSchedulerStrategyLegacy,
			expiresAt: 0,
		})
	})
}

func TestOpenAIGatewayService_ExplainOpenAIRoutingForAccount_NotFound(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		}},
	}

	result, err := svc.ExplainOpenAIRoutingForAccount(context.Background(), 74002, OpenAIRoutingExplainParams{})

	require.Nil(t, result)
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_StrictPriorityMarksLowerPriorityLayer(t *testing.T) {
	setOpenAIRoutingExplainStrategyForTest(t, OpenAIAccountSchedulerStrategyStrictPriority)
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74031, Name: "disabled-high", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled, Schedulable: false, Priority: 1, Concurrency: 1},
			{ID: 74032, Name: "active-top", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 1},
			{ID: 74033, Name: "active-lower", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 20, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, OpenAIAccountSchedulerStrategyStrictPriority, result.SchedulerStrategy)
	require.Equal(t, "strict_priority_snapshot", result.Source)
	require.True(t, result.StrictPriority.Enabled)
	require.NotNil(t, result.StrictPriority.CurrentAvailablePriority)
	require.Equal(t, 5, *result.StrictPriority.CurrentAvailablePriority)
	require.Equal(t, 2, result.StrictPriority.CandidateCount)
	require.Len(t, result.StrictPriority.ExcludedAccounts, 1)
	require.Equal(t, int64(74033), result.StrictPriority.ExcludedAccounts[0].AccountID)
	require.Equal(t, 20, result.StrictPriority.ExcludedAccounts[0].Priority)
	require.Equal(t, 5, result.StrictPriority.ExcludedAccounts[0].CurrentPriority)
	require.Contains(t, result.StrictPriority.ExcludedAccounts[0].Reasons, "strict_priority_lower_tier")

	byID := map[int64]OpenAIRoutingSummary{}
	for _, item := range result.Items {
		byID[item.AccountID] = item
	}
	require.True(t, byID[74032].IsSchedulableNow)
	require.Equal(t, 1, byID[74032].Rank)
	require.False(t, byID[74033].IsSchedulableNow)
	require.Equal(t, "strict_priority_lower_tier", byID[74033].SummaryReason)
	require.Contains(t, byID[74033].BlockReasons, "strict_priority_lower_tier")
	require.False(t, byID[74031].IsSchedulableNow)
	require.Contains(t, byID[74031].BlockReasons, "status_disabled")
	require.NotContains(t, byID[74031].BlockReasons, "strict_priority_lower_tier")

	accountExplain, err := svc.ExplainOpenAIRoutingForAccount(context.Background(), 74033, OpenAIRoutingExplainParams{})
	require.NoError(t, err)
	require.NotNil(t, accountExplain)
	require.Equal(t, OpenAIAccountSchedulerStrategyStrictPriority, accountExplain.SchedulerStrategy)
	require.Equal(t, "strict_priority_lower_tier", accountExplain.Account.SummaryReason)
	require.True(t, accountExplain.StrictPriority.Enabled)
	require.Len(t, accountExplain.Top, 1)
	require.Equal(t, int64(74032), accountExplain.Top[0].AccountID)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_StrictPriorityRanksTopLayerByLastUsed(t *testing.T) {
	setOpenAIRoutingExplainStrategyForTest(t, OpenAIAccountSchedulerStrategyStrictPriority)
	older := time.Date(2026, 6, 27, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74051, Name: "top-newer", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1, LastUsedAt: &newer},
			{ID: 74052, Name: "top-never", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1},
			{ID: 74053, Name: "top-older", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1, LastUsedAt: &older},
			{ID: 74054, Name: "lower-never", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{})

	require.NoError(t, err)
	require.Len(t, result.Items, 4)
	require.Equal(t, int64(74052), result.Items[0].AccountID)
	require.Equal(t, "strict_priority_top_tier", result.Items[0].SummaryReason)
	require.Contains(t, result.Items[0].SummaryReasons, "strict_priority_never_used_first")
	require.Equal(t, 1, result.Items[0].Rank)
	require.Equal(t, int64(74053), result.Items[1].AccountID)
	require.Contains(t, result.Items[1].SummaryReasons, "strict_priority_least_recently_used")
	require.Equal(t, 2, result.Items[1].Rank)
	require.Equal(t, int64(74051), result.Items[2].AccountID)
	require.Equal(t, 3, result.Items[2].Rank)
	require.Equal(t, int64(74054), result.Items[3].AccountID)
	require.False(t, result.Items[3].IsSchedulableNow)
}

func TestOpenAIGatewayService_ExplainOpenAIRoutingForAccount_StrictPriorityNotesAndTopCandidates(t *testing.T) {
	setOpenAIRoutingExplainStrategyForTest(t, OpenAIAccountSchedulerStrategyStrictPriority)
	older := time.Date(2026, 6, 27, 8, 0, 0, 0, time.UTC)
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74061, Name: "top-old", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 1, LastUsedAt: &older},
			{ID: 74062, Name: "top-never", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0, Concurrency: 1},
			{ID: 74063, Name: "lower", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 3, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRoutingForAccount(context.Background(), 74063, OpenAIRoutingExplainParams{})

	require.NoError(t, err)
	require.Equal(t, OpenAIAccountSchedulerStrategyStrictPriority, result.SchedulerStrategy)
	require.Equal(t, int64(74063), result.Account.AccountID)
	require.False(t, result.Account.IsSchedulableNow)
	require.ElementsMatch(t, []string{"strict_priority", "strict_priority_top_tier_only", "strict_priority_same_tier_last_used"}, result.Notes)
	require.Len(t, result.Top, 2)
	require.Equal(t, int64(74062), result.Top[0].AccountID)
	require.Equal(t, int64(74061), result.Top[1].AccountID)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_LegacyAndExperimentalDoNotApplyStrictLayer(t *testing.T) {
	for _, strategy := range []string{OpenAIAccountSchedulerStrategyLegacy, OpenAIAccountSchedulerStrategyExperimental} {
		t.Run(strategy, func(t *testing.T) {
			setOpenAIRoutingExplainStrategyForTest(t, strategy)
			svc := &OpenAIGatewayService{
				accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
					{ID: 74041, Name: "active-top", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 1},
					{ID: 74042, Name: "active-lower", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 20, Concurrency: 1},
				}},
			}

			result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{})

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, strategy, result.SchedulerStrategy)
			require.False(t, result.StrictPriority.Enabled)
			require.Nil(t, result.StrictPriority.CurrentAvailablePriority)
			require.Empty(t, result.StrictPriority.ExcludedAccounts)
			require.Len(t, result.Items, 2)
			for _, item := range result.Items {
				require.True(t, item.IsSchedulableNow)
				require.NotContains(t, item.BlockReasons, "strict_priority_lower_tier")
			}
		})
	}
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_ExposesPriceSource(t *testing.T) {
	setOpenAIRoutingExplainStrategyForTest(t, OpenAIAccountSchedulerStrategyExperimental)
	accountRate := 0.2
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{
				ID:             74081,
				Name:           "effective-price",
				Platform:       PlatformOpenAI,
				Type:           AccountTypeAPIKey,
				Status:         StatusActive,
				Schedulable:    true,
				Concurrency:    1,
				RateMultiplier: &accountRate,
				Extra: map[string]any{
					"upstream_effective_rate_multiplier": 0.08,
					"upstream_group_rate_multiplier":     0.12,
				},
			},
			{
				ID:             74082,
				Name:           "group-price",
				Platform:       PlatformOpenAI,
				Type:           AccountTypeAPIKey,
				Status:         StatusActive,
				Schedulable:    true,
				Concurrency:    1,
				RateMultiplier: &accountRate,
				Extra: map[string]any{
					"upstream_effective_rate_multiplier": -1,
					"upstream_group_rate_multiplier":     "0.12",
				},
			},
			{
				ID:             74083,
				Name:           "account-price",
				Platform:       PlatformOpenAI,
				Type:           AccountTypeAPIKey,
				Status:         StatusActive,
				Schedulable:    true,
				Concurrency:    1,
				RateMultiplier: &accountRate,
			},
			{
				ID:          74084,
				Name:        "default-account-price",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{})

	require.NoError(t, err)
	byID := map[int64]OpenAIRoutingSummary{}
	for _, item := range result.Items {
		byID[item.AccountID] = item
	}
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         "upstream_effective_rate_multiplier",
		RateMultiplier: 0.08,
	}, byID[74081].PriceSource)
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         "upstream_group_rate_multiplier",
		RateMultiplier: 0.12,
	}, byID[74082].PriceSource)
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         "account.rate_multiplier",
		RateMultiplier: 0.2,
		Fallback:       true,
		FallbackReason: openAIRoutingPriceFallbackUpstreamRateMissing,
	}, byID[74083].PriceSource)
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         "account.rate_multiplier",
		RateMultiplier: 1,
		Fallback:       true,
		FallbackReason: openAIRoutingPriceFallbackAccountRateDefaultOne,
	}, byID[74084].PriceSource)
	require.Equal(t, 0.5, byID[74084].Score.Price)

	accountExplain, err := svc.ExplainOpenAIRoutingForAccount(context.Background(), 74082, OpenAIRoutingExplainParams{})
	require.NoError(t, err)
	require.Equal(t, "upstream_group_rate_multiplier", accountExplain.Account.PriceSource.Source)
	require.Equal(t, 0.12, accountExplain.Account.PriceSource.RateMultiplier)
	require.ElementsMatch(t, []string{
		"experimental_scheduler",
		"price_uses_upstream_effective_then_group_then_account_rate_multiplier",
	}, accountExplain.Notes)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_ExperimentalMarksCircuitOpenAccount(t *testing.T) {
	setOpenAIRoutingExplainStrategyForTest(t, OpenAIAccountSchedulerStrategyExperimental)
	stats := newOpenAIAccountRuntimeStats()
	stats.report(74071, false, nil)
	stats.report(74071, false, nil)
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74071, Name: "open-circuit", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1},
			{ID: 74072, Name: "healthy", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 5, Concurrency: 1},
		}},
		openaiAccountStats: stats,
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, OpenAIAccountSchedulerStrategyExperimental, result.SchedulerStrategy)
	byID := map[int64]OpenAIRoutingSummary{}
	for _, item := range result.Items {
		byID[item.AccountID] = item
	}
	require.False(t, byID[74071].IsSchedulableNow)
	require.Equal(t, "experimental_circuit_open", byID[74071].SummaryReason)
	require.Contains(t, byID[74071].BlockReasons, "experimental_circuit_open")
	require.True(t, byID[74072].IsSchedulableNow)
	require.Equal(t, 1, byID[74072].Rank)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_FiltersRequestedAccountIDs(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74011, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: 74012, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: 74013, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{AccountIDs: []int64{74012, 74013}, AccountIDsProvided: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	require.Equal(t, int64(74012), result.Items[0].AccountID)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_EmptyProvidedAccountIDsDoesNotFallbackToAll(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74021, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{AccountIDsProvided: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.Items)
}
