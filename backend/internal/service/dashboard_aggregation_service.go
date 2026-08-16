package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const (
	defaultDashboardAggregationTimeout         = 2 * time.Minute
	defaultDashboardAggregationBackfillTimeout = 30 * time.Minute
	dashboardAggregationRetentionInterval      = 6 * time.Hour

	// dashboardAggregationLeaderLockKey gates the periodic scheduled aggregation so
	// that only one instance runs it per cycle in a multi-replica deployment.
	dashboardAggregationLeaderLockKey = "dashboard:aggregation:leader"
	// dashboardAggregationLeaderLockTTL must exceed the job's worst-case runtime
	// (defaultDashboardAggregationTimeout) so the lock never expires mid-run.
	dashboardAggregationLeaderLockTTL                   = 5 * time.Minute
	dashboardAggregationGroupUsageBackfillLeaderLockKey = "dashboard:aggregation:group-usage-backfill:leader"
	dashboardAggregationGroupUsageBackfillLeaderLockTTL = defaultDashboardAggregationBackfillTimeout + time.Minute
	accountCostMaintenanceLeaderLockKey                 = "dashboard:account-cost-maintenance:leader"
	accountCostMaintenanceLeaderLockTTL                 = 5 * time.Minute
	accountCostLedgerRunBudget                          = 2 * time.Minute
	accountCostAggregateRunBudget                       = 2 * time.Minute
	accountCostMaintenanceInterval                      = 10 * time.Minute
	accountCostLedgerMaxBatches                         = 128
	accountCostAggregateMaxChunks                       = 128
	accountCostBackfillYield                            = 100 * time.Millisecond
	accountCostBackfillLogTimeout                       = 5 * time.Second
	accountCostTotalsBatchSize                          = int64(10000)
)

var (
	// ErrDashboardBackfillDisabled 当配置禁用回填时返回。
	ErrDashboardBackfillDisabled = errors.New("仪表盘聚合回填已禁用")
	// ErrDashboardBackfillTooLarge 当回填跨度超过限制时返回。
	ErrDashboardBackfillTooLarge   = errors.New("回填时间跨度过大")
	errDashboardAggregationRunning = errors.New("聚合作业正在运行")
)

// DashboardAggregationRepository 定义仪表盘预聚合仓储接口。
type DashboardAggregationRepository interface {
	AggregateRange(ctx context.Context, start, end time.Time) error
	AggregateAccountCostRange(ctx context.Context, start, end time.Time) error
	// ProcessAccountCostTotals 在一个事务中处理一个账号的一段 usage_logs.id，
	// 并推进该账号的累计账本检查点。新增日志只处理检查点之后的增量。
	ProcessAccountCostTotals(ctx context.Context, batchSize int64) (int64, error)
	GetAccountCostAggregationState(ctx context.Context) (AccountCostAggregationState, error)
	RefreshDashboardCostSnapshot(ctx context.Context, targetStart, targetEnd time.Time) (bool, error)
	// RecomputeRange 重新计算指定时间范围内的聚合数据（包含活跃用户等派生表）。
	// 设计目的：当 usage_logs 被批量删除/回滚后，确保聚合表可恢复一致性。
	RecomputeRange(ctx context.Context, start, end time.Time) error
	GetAggregationWatermark(ctx context.Context) (time.Time, error)
	GetAccountCostAggregationCoverage(ctx context.Context) (time.Time, time.Time, error)
	UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error
	CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error
	CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error
	EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error
}

// dashboardCostSnapshotStaler is an optional repository capability. Keeping it
// optional avoids making lightweight aggregation test doubles implement a
// persistence-only invalidation operation.
type dashboardCostSnapshotStaler interface {
	MarkDashboardCostSnapshotStale(ctx context.Context) error
}

// DashboardCostSnapshotRefresher is the narrow dependency used by account
// management after an operator changes an account's CNY cost. The snapshot is
// derived from accounts plus the already-materialized usage totals, so it can
// be refreshed directly without re-scanning usage_logs.
type DashboardCostSnapshotRefresher interface {
	RefreshDashboardCostSnapshotAfterAccountCostChange()
}

// AccountCostAggregationState 描述账号累计成本账本的后台进度。
type AccountCostAggregationState struct {
	LastProcessedUsageID int64
	TotalAccounts        int64
	PendingAccounts      int64
	BackfillComplete     bool
	ComputedAt           time.Time
}

// DashboardAggregationService 负责定时聚合与回填。
type DashboardAggregationService struct {
	repo                          DashboardAggregationRepository
	timingWheel                   *TimingWheelService
	cfg                           config.DashboardAggregationConfig
	running                       int32
	accountCostLedgerRunning      int32
	accountCostBackfillRunning    int32
	accountCostMaintenanceRunning int32
	lastRetentionCleanup          atomic.Value // time.Time
	accountCostBackfillYieldFn    func(context.Context) bool

	lockCache      LeaderLockCache
	db             *sql.DB
	instanceID     string
	dashboardCache DashboardStatsCache
}

// NewDashboardAggregationService 创建聚合服务。
func NewDashboardAggregationService(repo DashboardAggregationRepository, timingWheel *TimingWheelService, cfg *config.Config) *DashboardAggregationService {
	var aggCfg config.DashboardAggregationConfig
	if cfg != nil {
		aggCfg = cfg.DashboardAgg
	}
	return &DashboardAggregationService{
		repo:        repo,
		timingWheel: timingWheel,
		cfg:         aggCfg,
		instanceID:  uuid.NewString(),
	}
}

// SetLeaderLock injects the leader-lock cache and DB used to elect a single
// instance for the periodic scheduled aggregation. When both are nil the job runs
// ungated (single-instance / test behavior).
func (s *DashboardAggregationService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// SetDashboardCache injects the shared dashboard cache so explicit usage-log
// cleanup cannot serve a cost summary built before the deletion.
func (s *DashboardAggregationService) SetDashboardCache(cache DashboardStatsCache) {
	if s == nil {
		return
	}
	s.dashboardCache = cache
}

// Start 启动定时聚合作业（重启生效配置）。
func (s *DashboardAggregationService) Start() {
	if s == nil || s.repo == nil || s.timingWheel == nil {
		return
	}
	if !s.cfg.Enabled {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 聚合作业已禁用")
		return
	}
	go s.runStartupGroupUsageSync()

	interval := time.Duration(s.cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}

	if s.cfg.RecomputeDays > 0 {
		go func() {
			s.runAccountCostMaintenance()
			s.recomputeRecentDays()
		}()
	} else {
		go s.runAccountCostMaintenance()
	}

	s.timingWheel.ScheduleRecurring("dashboard:aggregation", interval, func() {
		s.runScheduledAggregation()
	})
	s.timingWheel.ScheduleRecurring("dashboard:account-cost-maintenance", accountCostMaintenanceInterval, func() {
		go s.runAccountCostMaintenance()
	})
	logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 聚合作业启动 (interval=%v, lookback=%ds)", interval, s.cfg.LookbackSeconds)
	if !s.cfg.BackfillEnabled {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 回填已禁用，如需补齐保留窗口以外历史数据请手动回填")
	}
}

func (s *DashboardAggregationService) runAccountCostMaintenance() {
	if s == nil || s.repo == nil || !s.cfg.Enabled {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.accountCostMaintenanceRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.accountCostMaintenanceRunning, 0)

	ctx, cancel := context.WithTimeout(context.Background(), accountCostMaintenanceLeaderLockTTL)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(
		ctx,
		s.lockCache,
		s.db,
		accountCostMaintenanceLeaderLockKey,
		s.instanceID,
		accountCostMaintenanceLeaderLockTTL,
	)
	if !ok {
		return
	}
	defer release()

	s.processAccountCostTotals()
	s.backfillAccountCostAggregates()
}

func (s *DashboardAggregationService) backfillAccountCostAggregates() {
	if s == nil || s.repo == nil || !s.cfg.Enabled {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.accountCostBackfillRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.accountCostBackfillRunning, 0)

	ctx, cancel := context.WithTimeout(context.Background(), accountCostAggregateRunBudget)
	defer cancel()

	// Historical chunks must align to complete hours. Using a minute/second
	// boundary would split one hour across adjacent descending chunks and let the
	// later upsert overwrite part of that hour.
	now := time.Now().UTC().Truncate(time.Hour)
	retentionDays := s.cfg.Retention.UsageLogsDays
	if retentionDays <= 0 {
		retentionDays = 1
	}
	targetStart := truncateToDayUTC(now.AddDate(0, 0, -retentionDays))
	coverageStart, coverageEnd, err := s.repo.GetAccountCostAggregationCoverage(ctx)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 读取账号成本聚合覆盖范围失败: %v", err)
		return
	}

	epoch := time.Unix(0, 0).UTC()
	runStartedAt := time.Now()
	deadline := runStartedAt.Add(accountCostAggregateRunBudget)
	processedChunks := 0
	defer func() {
		s.refreshDashboardCostSnapshot(targetStart, now)
		s.logAccountCostBackfillProgress(targetStart, now, processedChunks, runStartedAt)
	}()
	aggregateChunk := func(start, end time.Time) bool {
		if processedChunks >= accountCostAggregateMaxChunks || (processedChunks > 0 && time.Now().After(deadline)) {
			return false
		}
		if !s.aggregateAccountCostBackfillChunk(ctx, start, end) {
			return false
		}
		processedChunks++
		if processedChunks < accountCostAggregateMaxChunks && !s.yieldAccountCostBackfill(ctx) {
			return false
		}
		return true
	}
	if !coverageEnd.After(epoch) || !coverageEnd.After(coverageStart) {
		cursor := now
		for cursor.After(targetStart) {
			windowStart := cursor.Add(-24 * time.Hour)
			if windowStart.Before(targetStart) {
				windowStart = targetStart
			}
			if !aggregateChunk(windowStart, cursor) {
				return
			}
			cursor = windowStart
		}
		return
	}

	// Always close the realtime tail first. Historical backfill must never leave
	// the hottest recent range on the request-time fallback path.
	cursor := coverageEnd
	for cursor.Before(now) {
		windowEnd := cursor.Add(24 * time.Hour)
		if windowEnd.After(now) {
			windowEnd = now
		}
		if !aggregateChunk(cursor, windowEnd) {
			return
		}
		cursor = windowEnd
	}

	cursor = coverageStart
	for cursor.After(targetStart) {
		windowStart := cursor.Add(-24 * time.Hour)
		if windowStart.Before(targetStart) {
			windowStart = targetStart
		}
		if !aggregateChunk(windowStart, cursor) {
			return
		}
		cursor = windowStart
	}
}

func (s *DashboardAggregationService) processAccountCostTotals() {
	if s == nil || s.repo == nil || !s.cfg.Enabled {
		return
	}
	if !atomic.CompareAndSwapInt32(&s.accountCostLedgerRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.accountCostLedgerRunning, 0)

	ctx, cancel := context.WithTimeout(context.Background(), accountCostLedgerRunBudget)
	defer cancel()
	s.processAccountCostTotalsBackfill(ctx)
}

func (s *DashboardAggregationService) processAccountCostTotalsBackfill(ctx context.Context) {
	startedAt := time.Now()
	processedBatches := 0
	for processedBatches < accountCostLedgerMaxBatches {
		if ctx.Err() != nil {
			break
		}
		processed, ok := s.processAccountCostTotalsChunk(ctx)
		if !ok {
			break
		}
		processedBatches++
		if processed == 0 || processedBatches >= accountCostLedgerMaxBatches || !s.yieldAccountCostBackfill(ctx) {
			break
		}
	}

	logCtx, cancel := context.WithTimeout(context.Background(), accountCostBackfillLogTimeout)
	defer cancel()
	state, err := s.repo.GetAccountCostAggregationState(logCtx)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 读取累计成本账本进度失败: %v", err)
		return
	}
	logger.LegacyPrintf(
		"service.dashboard_aggregation",
		"[DashboardAggregation] 累计成本账本进度 (batches=%d last_id=%d total_accounts=%d pending_accounts=%d complete=%t duration=%s)",
		processedBatches,
		state.LastProcessedUsageID,
		state.TotalAccounts,
		state.PendingAccounts,
		state.BackfillComplete,
		time.Since(startedAt).String(),
	)
}

func (s *DashboardAggregationService) processAccountCostTotalsChunk(ctx context.Context) (int64, bool) {
	processed, err := s.repo.ProcessAccountCostTotals(ctx, accountCostTotalsBatchSize)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 累计成本账本批次失败: %v", err)
		return 0, false
	}
	return processed, true
}
func (s *DashboardAggregationService) refreshDashboardCostSnapshot(targetStart, targetEnd time.Time) {
	if s == nil || s.repo == nil || !targetEnd.After(targetStart) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountCostBackfillLogTimeout)
	defer cancel()
	complete, err := s.repo.RefreshDashboardCostSnapshot(ctx, targetStart, targetEnd)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 刷新成本快照失败: %v", err)
		return
	}
	if !complete {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 成本快照等待完整覆盖 (target_start=%s target_end=%s)", targetStart.UTC().Format(time.RFC3339), targetEnd.UTC().Format(time.RFC3339))
	}
}

// RefreshDashboardCostSnapshotAfterAccountCostChange makes an account-cost
// edit visible on the dashboard immediately. Account cost is part of the
// snapshot calculation but does not change historical usage aggregates, so a
// full aggregation recompute would be unnecessary and expensive.
func (s *DashboardAggregationService) RefreshDashboardCostSnapshotAfterAccountCostChange() {
	if s == nil || s.repo == nil {
		return
	}
	s.invalidateDashboardCostSnapshot()
	now := time.Now().UTC()
	retentionDays := s.cfg.Retention.UsageLogsDays
	if retentionDays <= 0 {
		retentionDays = 1
	}
	s.refreshDashboardCostSnapshot(truncateToDayUTC(now.AddDate(0, 0, -retentionDays)), now)
}

func (s *DashboardAggregationService) yieldAccountCostBackfill(ctx context.Context) bool {
	if s.accountCostBackfillYieldFn != nil {
		return s.accountCostBackfillYieldFn(ctx)
	}
	timer := time.NewTimer(accountCostBackfillYield)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *DashboardAggregationService) logAccountCostBackfillProgress(targetStart, targetEnd time.Time, processedChunks int, startedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), accountCostBackfillLogTimeout)
	defer cancel()
	coverageStart, coverageEnd, err := s.repo.GetAccountCostAggregationCoverage(ctx)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 读取账号成本回填进度失败: %v", err)
		return
	}
	complete := !coverageStart.After(targetStart) && !coverageEnd.Before(targetEnd)
	logger.LegacyPrintf(
		"service.dashboard_aggregation",
		"[DashboardAggregation] 账号成本回填进度 (chunks=%d coverage_start=%s coverage_end=%s target_start=%s complete=%t duration=%s)",
		processedChunks,
		coverageStart.UTC().Format(time.RFC3339),
		coverageEnd.UTC().Format(time.RFC3339),
		targetStart.UTC().Format(time.RFC3339),
		complete,
		time.Since(startedAt).String(),
	)
}

func (s *DashboardAggregationService) aggregateAccountCostBackfillChunk(ctx context.Context, start, end time.Time) bool {
	// Account-cost chunks share the dashboard aggregation lock only while writing
	// their bounded range, allowing realtime aggregation to run between chunks.
	for ctx.Err() == nil {
		if atomic.CompareAndSwapInt32(&s.running, 0, 1) {
			release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, dashboardAggregationLeaderLockKey, s.instanceID, dashboardAggregationLeaderLockTTL)
			if ok {
				err := s.repo.AggregateAccountCostRange(ctx, start, end)
				release()
				atomic.StoreInt32(&s.running, 0)
				if err != nil {
					logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 账号成本聚合块失败 (start=%s end=%s): %v", start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), err)
					return false
				}
				return true
			}
			atomic.StoreInt32(&s.running, 0)
		}

		timer := time.NewTimer(accountCostBackfillYield)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}

// TriggerBackfill 触发回填（异步）。
func (s *DashboardAggregationService) TriggerBackfill(start, end time.Time) error {
	if s == nil || s.repo == nil {
		return errors.New("聚合服务未初始化")
	}
	if !s.cfg.BackfillEnabled {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 回填被拒绝: backfill_enabled=false")
		return ErrDashboardBackfillDisabled
	}
	if !end.After(start) {
		return errors.New("回填时间范围无效")
	}
	if s.cfg.BackfillMaxDays > 0 {
		maxRange := time.Duration(s.cfg.BackfillMaxDays) * 24 * time.Hour
		if end.Sub(start) > maxRange {
			return ErrDashboardBackfillTooLarge
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationBackfillTimeout)
		defer cancel()
		if err := s.backfillRange(ctx, start, end); err != nil {
			logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 回填失败: %v", err)
		}
	}()
	return nil
}

// TriggerRecomputeRange 触发指定范围的重新计算（异步）。
// 与 TriggerBackfill 不同：
// - 不依赖 backfill_enabled（这是内部一致性修复）
// - 不更新 watermark（避免影响正常增量聚合游标）
func (s *DashboardAggregationService) TriggerRecomputeRange(start, end time.Time) error {
	if s == nil || s.repo == nil {
		return errors.New("聚合服务未初始化")
	}
	if !end.After(start) {
		return errors.New("重新计算时间范围无效")
	}
	s.invalidateDashboardCostSnapshot()
	if !s.cfg.Enabled {
		return errors.New("聚合服务已禁用")
	}

	go func() {
		const maxRetries = 3
		for i := 0; i < maxRetries; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationBackfillTimeout)
			err := s.recomputeRange(ctx, start, end)
			cancel()
			if err == nil {
				return
			}
			if !errors.Is(err, errDashboardAggregationRunning) {
				logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 重新计算失败: %v", err)
				return
			}
			time.Sleep(5 * time.Second)
		}
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 重新计算放弃: 聚合作业持续占用")
	}()
	return nil
}

func (s *DashboardAggregationService) recomputeRecentDays() {
	days := s.cfg.RecomputeDays
	if days <= 0 {
		return
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -days)

	ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationBackfillTimeout)
	defer cancel()
	if err := s.backfillRange(ctx, start, now); err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 启动重算失败: %v", err)
		return
	}
}

func (s *DashboardAggregationService) recomputeRange(ctx context.Context, start, end time.Time) error {
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return errDashboardAggregationRunning
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now().UTC()
	if err := s.repo.RecomputeRange(ctx, start, end); err != nil {
		return err
	}
	s.refreshDashboardCostSnapshot(start, end)
	logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 重新计算完成 (start=%s end=%s duration=%s)",
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
		time.Since(jobStart).String(),
	)
	return nil
}

func (s *DashboardAggregationService) invalidateDashboardCostSnapshot() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountCostBackfillLogTimeout)
	defer cancel()
	if staler, ok := s.repo.(dashboardCostSnapshotStaler); ok {
		if err := staler.MarkDashboardCostSnapshotStale(ctx); err != nil {
			logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 标记成本快照过期失败: %v", err)
		}
	}
	if s.dashboardCache != nil {
		if err := s.dashboardCache.DeleteDashboardCostSummary(ctx); err != nil {
			logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 删除成本快照缓存失败: %v", err)
		}
	}
}

func (s *DashboardAggregationService) runScheduledAggregation() {
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationTimeout)
	defer cancel()

	// Multi-instance guard: only the leader runs the periodic aggregation; peers
	// skip this cycle to avoid N× redundant GROUP BY queries and watermark races.
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, dashboardAggregationLeaderLockKey, s.instanceID, dashboardAggregationLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	defer s.runScheduledGroupUsageSync()

	now := time.Now().UTC()
	last, err := s.repo.GetAggregationWatermark(ctx)
	if err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 读取水位失败: %v", err)
		last = time.Unix(0, 0).UTC()
	}

	lookback := time.Duration(s.cfg.LookbackSeconds) * time.Second
	epoch := time.Unix(0, 0).UTC()
	start := last.Add(-lookback)
	if !last.After(epoch) {
		retentionDays := s.cfg.Retention.UsageLogsDays
		if retentionDays <= 0 {
			retentionDays = 1
		}
		start = truncateToDayUTC(now.AddDate(0, 0, -retentionDays))
	} else if start.After(now) {
		start = now.Add(-lookback)
	}

	if err := s.aggregateRange(ctx, start, now); err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 聚合失败: %v", err)
		return
	}

	updateErr := s.repo.UpdateAggregationWatermark(ctx, now)
	if updateErr != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 更新水位失败: %v", updateErr)
	}
	slog.Debug("[DashboardAggregation] 聚合完成",
		"start", start.Format(time.RFC3339),
		"end", now.Format(time.RFC3339),
		"duration", time.Since(jobStart).String(),
		"watermark_updated", updateErr == nil,
	)
	s.maybeCleanupRetention(ctx, now)
}

func (s *DashboardAggregationService) runScheduledGroupUsageSync() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationTimeout)
	defer cancel()
	if err := s.syncGroupUsageRollups(ctx, time.Now().UTC()); err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 分组用量日汇总失败: %v", err)
	}
}

func (s *DashboardAggregationService) runStartupGroupUsageSync() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDashboardAggregationBackfillTimeout)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, dashboardAggregationGroupUsageBackfillLeaderLockKey, s.instanceID, dashboardAggregationGroupUsageBackfillLeaderLockTTL)
	if !ok {
		return
	}
	defer release()
	if err := s.syncGroupUsageRollups(ctx, time.Now().UTC()); err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 启动分组用量回填失败: %v", err)
	}
}

func (s *DashboardAggregationService) syncGroupUsageRollups(ctx context.Context, now time.Time) error {
	repo, ok := s.repo.(GroupUsageRollupRepository)
	if !ok {
		return nil
	}
	return repo.SyncGroupUsageRollups(ctx, GroupUsageTodayStart(now))
}

func (s *DashboardAggregationService) backfillRange(ctx context.Context, start, end time.Time) error {
	if !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return errDashboardAggregationRunning
	}
	defer atomic.StoreInt32(&s.running, 0)

	jobStart := time.Now().UTC()
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return errors.New("回填时间范围无效")
	}

	cursor := truncateToDayUTC(startUTC)
	for cursor.Before(endUTC) {
		windowEnd := cursor.Add(24 * time.Hour)
		if windowEnd.After(endUTC) {
			windowEnd = endUTC
		}
		if err := s.aggregateRange(ctx, cursor, windowEnd); err != nil {
			return err
		}
		cursor = windowEnd
	}

	updateErr := s.repo.UpdateAggregationWatermark(ctx, endUTC)
	if updateErr != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 更新水位失败: %v", updateErr)
	}
	logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 回填聚合完成 (start=%s end=%s duration=%s watermark_updated=%t)",
		startUTC.Format(time.RFC3339),
		endUTC.Format(time.RFC3339),
		time.Since(jobStart).String(),
		updateErr == nil,
	)

	s.maybeCleanupRetention(ctx, endUTC)
	return nil
}

func (s *DashboardAggregationService) aggregateRange(ctx context.Context, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	if err := s.repo.EnsureUsageLogsPartitions(ctx, end); err != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 分区检查失败: %v", err)
	}
	return s.repo.AggregateRange(ctx, start, end)
}

func (s *DashboardAggregationService) maybeCleanupRetention(ctx context.Context, now time.Time) {
	// Cost aggregation is deliberately read-only with respect to usage_logs.
	// Raw-log deletion, when explicitly requested, belongs to UsageCleanupService
	// and is never triggered by the dashboard/account-cost scheduler.
	lastAny := s.lastRetentionCleanup.Load()
	if lastAny != nil {
		if last, ok := lastAny.(time.Time); ok && now.Sub(last) < dashboardAggregationRetentionInterval {
			return
		}
	}

	hourlyCutoff := now.AddDate(0, 0, -s.cfg.Retention.HourlyDays)
	dailyCutoff := now.AddDate(0, 0, -s.cfg.Retention.DailyDays)
	dedupCutoff := now.AddDate(0, 0, -s.cfg.Retention.UsageBillingDedupDays)

	aggErr := s.repo.CleanupAggregates(ctx, hourlyCutoff, dailyCutoff)
	if aggErr != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] 聚合保留清理失败: %v", aggErr)
	}
	dedupErr := s.repo.CleanupUsageBillingDedup(ctx, dedupCutoff)
	if dedupErr != nil {
		logger.LegacyPrintf("service.dashboard_aggregation", "[DashboardAggregation] usage_billing_dedup 保留清理失败: %v", dedupErr)
	}
	if aggErr == nil && dedupErr == nil {
		s.lastRetentionCleanup.Store(now)
	}
}

func truncateToDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
