package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type opsStatsAccountRepoStub struct {
	AccountRepository
	accounts []Account
}

func (r opsStatsAccountRepoStub) ListOpsAccountsForStats(_ context.Context, _ string, _ *int64) ([]Account, error) {
	return r.accounts, nil
}

func TestOpsAvailabilityStatsExcludeArchivedAccounts(t *testing.T) {
	now := time.Now()
	group := &Group{ID: 10, Name: "default", Platform: PlatformOpenAI}
	svc := &OpsService{
		accountRepo: opsStatsAccountRepoStub{
			accounts: []Account{
				{
					ID:          1,
					Name:        "visible",
					Platform:    PlatformOpenAI,
					Status:      StatusActive,
					Schedulable: true,
					Groups:      []*Group{group},
				},
				{
					ID:          2,
					Name:        "archived-error",
					Platform:    PlatformOpenAI,
					Status:      StatusError,
					ArchivedAt:  &now,
					Schedulable: false,
					Groups:      []*Group{group},
				},
			},
		},
	}

	platformStats, groupStats, accountStats, _, err := svc.GetAccountAvailabilityStats(context.Background(), "", nil)
	require.NoError(t, err)

	require.Contains(t, accountStats, int64(1))
	require.NotContains(t, accountStats, int64(2))

	require.Equal(t, int64(1), platformStats[PlatformOpenAI].TotalAccounts)
	require.Equal(t, int64(1), platformStats[PlatformOpenAI].AvailableCount)
	require.Equal(t, int64(0), platformStats[PlatformOpenAI].ErrorCount)

	require.Equal(t, int64(1), groupStats[group.ID].TotalAccounts)
	require.Equal(t, int64(1), groupStats[group.ID].AvailableCount)
	require.Equal(t, int64(0), groupStats[group.ID].ErrorCount)
}
