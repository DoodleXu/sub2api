package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type dashboardAggregationRepoTestStub struct {
	aggregateCalls         int
	recomputeCalls         int
	cleanupUsageCalls      int
	cleanupDedupCalls      int
	ensurePartitionCalls   int
	lastStart              time.Time
	lastEnd                time.Time
	watermark              time.Time
	aggregateErr           error
	cleanupAggregatesErr   error
	cleanupUsageErr        error
	cleanupDedupErr        error
	ensurePartitionErr     error
	accountCostRanges      [][2]time.Time
	accountCoverageStart   time.Time
	accountCoverageEnd     time.Time
	refreshSnapshotCalls   int
	processTotalsCalls     int
	processTotalsResult    int64
	markSnapshotStaleCalls int32
	recomputeDoneCalls     int32
	refreshDoneCalls       int32
}

func (s *dashboardAggregationRepoTestStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	s.aggregateCalls++
	s.lastStart = start
	s.lastEnd = end
	return s.aggregateErr
}

func (s *dashboardAggregationRepoTestStub) AggregateAccountCostRange(ctx context.Context, start, end time.Time) error {
	s.accountCostRanges = append(s.accountCostRanges, [2]time.Time{start, end})
	return nil
}

func (s *dashboardAggregationRepoTestStub) ProcessAccountCostTotals(ctx context.Context, batchSize int64) (int64, error) {
	s.processTotalsCalls++
	result := s.processTotalsResult
	s.processTotalsResult = 0
	return result, nil
}

func (s *dashboardAggregationRepoTestStub) GetAccountCostAggregationState(ctx context.Context) (AccountCostAggregationState, error) {
	return AccountCostAggregationState{BackfillComplete: true}, nil
}

func (s *dashboardAggregationRepoTestStub) RefreshDashboardCostSnapshot(ctx context.Context, targetStart, targetEnd time.Time) (bool, error) {
	s.refreshSnapshotCalls++
	atomic.AddInt32(&s.refreshDoneCalls, 1)
	return true, nil
}

func (s *dashboardAggregationRepoTestStub) MarkDashboardCostSnapshotStale(ctx context.Context) error {
	atomic.AddInt32(&s.markSnapshotStaleCalls, 1)
	return nil
}

func (s *dashboardAggregationRepoTestStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	s.recomputeCalls++
	atomic.AddInt32(&s.recomputeDoneCalls, 1)
	return s.AggregateRange(ctx, start, end)
}

func (s *dashboardAggregationRepoTestStub) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	return s.watermark, nil
}

func (s *dashboardAggregationRepoTestStub) GetAccountCostAggregationCoverage(ctx context.Context) (time.Time, time.Time, error) {
	if !s.accountCoverageStart.IsZero() || !s.accountCoverageEnd.IsZero() {
		return s.accountCoverageStart, s.accountCoverageEnd, nil
	}
	epoch := time.Unix(0, 0).UTC()
	return epoch, epoch, nil
}

func (s *dashboardAggregationRepoTestStub) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoTestStub) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return s.cleanupAggregatesErr
}

func (s *dashboardAggregationRepoTestStub) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	s.cleanupUsageCalls++
	return s.cleanupUsageErr
}

func (s *dashboardAggregationRepoTestStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	s.cleanupDedupCalls++
	return s.cleanupDedupErr
}

func (s *dashboardAggregationRepoTestStub) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	s.ensurePartitionCalls++
	return s.ensurePartitionErr
}

func TestDashboardAggregationService_RunScheduledAggregation_EpochUsesRetentionStart(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{watermark: time.Unix(0, 0).UTC()}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.runScheduledAggregation()

	require.Equal(t, 1, repo.aggregateCalls)
	require.False(t, repo.lastEnd.IsZero())
	require.Equal(t, truncateToDayUTC(repo.lastEnd.AddDate(0, 0, -1)), repo.lastStart)
}

func TestDashboardAggregationService_RunScheduledAggregationProcessesAccountCostTotals(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{
		watermark:           time.Now().UTC().Add(-time.Minute),
		processTotalsResult: 7,
	}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Enabled:         true,
			LookbackSeconds: 120,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.runScheduledAggregation()

	require.Zero(t, repo.processTotalsCalls, "scheduled aggregation should not eagerly consume account cost ledger batches")
	require.Zero(t, repo.refreshSnapshotCalls, "scheduled aggregation should leave cost snapshot refresh to the 10-minute maintenance task")
}

func TestDashboardAggregationService_BackfillsAccountCostInDailyChunksWithoutGlobalReaggregation(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo:                       repo,
		accountCostBackfillYieldFn: func(context.Context) bool { return true },
		cfg: config.DashboardAggregationConfig{
			Enabled: true,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 2,
			},
		},
	}

	svc.backfillAccountCostAggregates()

	require.NotEmpty(t, repo.accountCostRanges)
	require.Equal(t, 0, repo.aggregateCalls)
	require.Equal(t, time.Now().UTC().Truncate(time.Hour), repo.accountCostRanges[0][1])
	for _, window := range repo.accountCostRanges {
		require.True(t, window[1].After(window[0]))
		require.LessOrEqual(t, window[1].Sub(window[0]), 24*time.Hour)
	}
}

func TestDashboardAggregationService_ResumesPartialAccountCostBackfillForward(t *testing.T) {
	now := time.Now().UTC()
	coverageStart := truncateToDayUTC(now.AddDate(0, 0, -2))
	coverageEnd := coverageStart.Add(24 * time.Hour)
	repo := &dashboardAggregationRepoTestStub{
		accountCoverageStart: coverageStart,
		accountCoverageEnd:   coverageEnd,
	}
	svc := &DashboardAggregationService{
		repo:                       repo,
		accountCostBackfillYieldFn: func(context.Context) bool { return true },
		cfg: config.DashboardAggregationConfig{
			Enabled: true,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 2,
			},
		},
	}

	svc.backfillAccountCostAggregates()

	require.NotEmpty(t, repo.accountCostRanges)
	require.True(t, repo.accountCostRanges[0][0].Equal(coverageEnd))
	require.Equal(t, 0, repo.aggregateCalls)
}

func TestDashboardAggregationService_BackfillsRetentionWindowWithinSafetyCap(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo:                       repo,
		accountCostBackfillYieldFn: func(context.Context) bool { return true },
		cfg: config.DashboardAggregationConfig{
			Enabled: true,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 30,
			},
		},
	}

	svc.backfillAccountCostAggregates()

	require.NotEmpty(t, repo.accountCostRanges)
	require.LessOrEqual(t, len(repo.accountCostRanges), accountCostAggregateMaxChunks)
	require.Greater(t, len(repo.accountCostRanges), 7, "fast backfill should not be artificially limited to seven days")
	require.Zero(t, atomic.LoadInt32(&svc.running))
	require.Zero(t, atomic.LoadInt32(&svc.accountCostBackfillRunning))
}

func TestDashboardAggregationService_AccountCostLedgerCadenceAndBudgets(t *testing.T) {
	require.Equal(t, 10*time.Minute, accountCostMaintenanceInterval)
	require.Equal(t, 2*time.Minute, accountCostLedgerRunBudget)
	require.Equal(t, 2*time.Minute, accountCostAggregateRunBudget)
	require.Equal(t, 128, accountCostLedgerMaxBatches)
	require.Equal(t, 128, accountCostAggregateMaxChunks)
}

func TestDashboardAggregationService_ProcessAccountCostTotalsUsesDedicatedRunner(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{processTotalsResult: 1}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg:  config.DashboardAggregationConfig{Enabled: true},
	}

	svc.processAccountCostTotals()

	require.Equal(t, 2, repo.processTotalsCalls, "runner should continue until the ledger reports no pending account")
	require.Zero(t, atomic.LoadInt32(&svc.accountCostLedgerRunning))
}

func TestDashboardAggregationService_AccountCostMaintenanceSkipsOnPeerLeader(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{processTotalsResult: 1}
	lockCache := &fakeLeaderLockCache{}
	held, err := lockCache.TryAcquireLeaderLock(
		context.Background(),
		accountCostMaintenanceLeaderLockKey,
		"peer",
		accountCostMaintenanceLeaderLockTTL,
	)
	require.NoError(t, err)
	require.True(t, held)
	svc := &DashboardAggregationService{
		repo:       repo,
		lockCache:  lockCache,
		instanceID: "local",
		cfg:        config.DashboardAggregationConfig{Enabled: true},
	}

	svc.runAccountCostMaintenance()

	require.Zero(t, repo.processTotalsCalls)
	require.Empty(t, repo.accountCostRanges)
}

func TestDashboardAggregationService_AccountCostMaintenanceUsesIndependentLeaderLock(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{processTotalsResult: 1}
	lockCache := &fakeLeaderLockCache{}
	svc := &DashboardAggregationService{
		repo:                       repo,
		lockCache:                  lockCache,
		instanceID:                 "local",
		accountCostBackfillYieldFn: func(context.Context) bool { return false },
		cfg: config.DashboardAggregationConfig{
			Enabled: true,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
			},
		},
	}

	svc.runAccountCostMaintenance()

	require.Equal(t, 1, repo.processTotalsCalls)
	require.Len(t, repo.accountCostRanges, 1)
	require.Empty(t, lockCache.heldBy(dashboardAggregationLeaderLockKey))
	require.Empty(t, lockCache.heldBy(accountCostMaintenanceLeaderLockKey))
	require.Zero(t, atomic.LoadInt32(&svc.accountCostMaintenanceRunning))
}

func TestDashboardAggregationService_AccountLedgerDoesNotWaitForDashboardLeader(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{processTotalsResult: 1}
	lockCache := &fakeLeaderLockCache{}
	held, err := lockCache.TryAcquireLeaderLock(
		context.Background(),
		dashboardAggregationLeaderLockKey,
		"dashboard-peer",
		dashboardAggregationLeaderLockTTL,
	)
	require.NoError(t, err)
	require.True(t, held)
	svc := &DashboardAggregationService{
		repo:      repo,
		lockCache: lockCache,
		cfg:       config.DashboardAggregationConfig{Enabled: true},
	}

	svc.processAccountCostTotals()

	require.Equal(t, 2, repo.processTotalsCalls)
}

func TestDashboardAggregationService_CleanupRetentionFailure_DoesNotRecord(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{cleanupAggregatesErr: errors.New("清理失败")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.maybeCleanupRetention(context.Background(), time.Now().UTC())

	require.Nil(t, svc.lastRetentionCleanup.Load())
	require.Zero(t, repo.cleanupUsageCalls, "cost aggregation must never delete raw usage logs")
	require.Equal(t, 1, repo.cleanupDedupCalls)
}

func TestDashboardAggregationService_CleanupDedupFailure_DoesNotRecord(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{cleanupDedupErr: errors.New("dedup cleanup failed")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 1,
				HourlyDays:    1,
				DailyDays:     1,
			},
		},
	}

	svc.maybeCleanupRetention(context.Background(), time.Now().UTC())

	require.Nil(t, svc.lastRetentionCleanup.Load())
	require.Zero(t, repo.cleanupUsageCalls, "cost aggregation must never delete raw usage logs")
	require.Equal(t, 1, repo.cleanupDedupCalls)
}

func TestDashboardAggregationService_PartitionFailure_DoesNotAggregate(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{ensurePartitionErr: errors.New("partition failed")}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays:         1,
				UsageBillingDedupDays: 2,
				HourlyDays:            1,
				DailyDays:             1,
			},
		},
	}

	svc.runScheduledAggregation()

	require.Equal(t, 1, repo.ensurePartitionCalls)
	require.Equal(t, 1, repo.aggregateCalls)
}

func TestDashboardAggregationService_TriggerBackfill_TooLarge(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	svc := &DashboardAggregationService{
		repo: repo,
		cfg: config.DashboardAggregationConfig{
			BackfillEnabled: true,
			BackfillMaxDays: 1,
		},
	}

	start := time.Now().AddDate(0, 0, -3)
	end := time.Now()
	err := svc.TriggerBackfill(start, end)
	require.ErrorIs(t, err, ErrDashboardBackfillTooLarge)
	require.Equal(t, 0, repo.aggregateCalls)
}

func TestDashboardAggregationService_TriggerRecomputeInvalidatesAndRefreshesCostSnapshot(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	cache := &dashboardCacheStub{}
	svc := NewDashboardAggregationService(repo, nil, &config.Config{
		DashboardAgg: config.DashboardAggregationConfig{Enabled: true},
	})
	svc.SetDashboardCache(cache)

	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	require.NoError(t, svc.TriggerRecomputeRange(start, end))

	require.Equal(t, int32(1), atomic.LoadInt32(&repo.markSnapshotStaleCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCostCalls))
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&repo.recomputeDoneCalls) == 1 && atomic.LoadInt32(&repo.refreshDoneCalls) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestDashboardAggregationService_AccountCostChangeRefreshesCostSnapshot(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	cache := &dashboardCacheStub{}
	svc := NewDashboardAggregationService(repo, nil, &config.Config{
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: 7},
		},
	})
	svc.SetDashboardCache(cache)

	svc.RefreshDashboardCostSnapshotAfterAccountCostChange()

	require.Equal(t, int32(1), atomic.LoadInt32(&repo.markSnapshotStaleCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCostCalls))
	require.Equal(t, 1, repo.refreshSnapshotCalls)
}

func TestDashboardAggregationService_DisabledRecomputeStillInvalidatesCostSnapshot(t *testing.T) {
	repo := &dashboardAggregationRepoTestStub{}
	cache := &dashboardCacheStub{}
	svc := NewDashboardAggregationService(repo, nil, &config.Config{})
	svc.SetDashboardCache(cache)

	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	require.Error(t, svc.TriggerRecomputeRange(start, start.Add(time.Hour)))
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.markSnapshotStaleCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCostCalls))
	require.Zero(t, atomic.LoadInt32(&repo.recomputeDoneCalls))
}
