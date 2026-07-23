package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedulerCostSnapshotCache struct {
	service.SchedulerCache
	account *service.Account
}

func (c schedulerCostSnapshotCache) GetAccount(context.Context, int64) (*service.Account, error) {
	return c.account, nil
}

func TestAccountUsesCostPerUSDForScheduling(t *testing.T) {
	unsupportedProbe := func() map[string]any {
		return map[string]any{
			service.UpstreamBillingProbeExtraKey: map[string]any{
				"status": service.UpstreamBillingProbeStatusUnsupported,
			},
		}
	}

	tests := []struct {
		name    string
		account *service.Account
		want    bool
	}{
		{
			name: "unsupported openai api key with cny cost",
			account: &service.Account{
				Platform:     service.PlatformOpenAI,
				Type:         service.AccountTypeAPIKey,
				TotalCostCNY: 6.95,
				Extra:        unsupportedProbe(),
			},
			want: true,
		},
		{
			name: "unsupported without cny cost",
			account: &service.Account{
				Platform: service.PlatformOpenAI,
				Type:     service.AccountTypeAPIKey,
				Extra:    unsupportedProbe(),
			},
		},
		{
			name: "successful probe",
			account: &service.Account{
				Platform:     service.PlatformOpenAI,
				Type:         service.AccountTypeAPIKey,
				TotalCostCNY: 6.95,
				Extra: map[string]any{
					service.UpstreamBillingProbeExtraKey: map[string]any{"status": service.UpstreamBillingProbeStatusOK},
				},
			},
		},
		{
			name: "openai oauth",
			account: &service.Account{
				Platform:     service.PlatformOpenAI,
				Type:         service.AccountTypeOAuth,
				TotalCostCNY: 6.95,
				Extra:        unsupportedProbe(),
			},
		},
		{
			name: "other platform",
			account: &service.Account{
				Platform:     service.PlatformAnthropic,
				Type:         service.AccountTypeAPIKey,
				TotalCostCNY: 6.95,
				Extra:        unsupportedProbe(),
			},
		},
		{name: "nil account"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, accountUsesCostPerUSDForScheduling(tt.account))
		})
	}
}

func TestPreserveCachedAccountCostStats(t *testing.T) {
	unsupported := func() map[string]any {
		return map[string]any{
			service.UpstreamBillingProbeExtraKey: map[string]any{
				"status": service.UpstreamBillingProbeStatusUnsupported,
			},
		}
	}
	target := &service.Account{
		ID:           1,
		Platform:     service.PlatformOpenAI,
		Type:         service.AccountTypeAPIKey,
		TotalCostCNY: 6.95,
		Extra:        unsupported(),
	}
	repo := &accountRepository{schedulerCache: schedulerCostSnapshotCache{account: &service.Account{
		ID:               1,
		TotalAccountCost: 100,
		CostCNYPerUSD:    999,
	}}}

	repo.preserveCachedAccountCostStats(context.Background(), target)

	require.Equal(t, 100.0, target.TotalAccountCost)
	require.Equal(t, 0.0695, target.CostCNYPerUSD)
}
