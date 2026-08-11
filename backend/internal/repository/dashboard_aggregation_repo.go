package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type dashboardAggregationRepository struct {
	sql sqlExecutor
}

const usageBillingDedupCleanupBatchSize = 10000

// NewDashboardAggregationRepository 创建仪表盘预聚合仓储。
func NewDashboardAggregationRepository(sqlDB *sql.DB) service.DashboardAggregationRepository {
	if sqlDB == nil {
		return nil
	}
	if !isPostgresDriver(sqlDB) {
		log.Printf("[DashboardAggregation] 检测到非 PostgreSQL 驱动，已自动禁用预聚合")
		return nil
	}
	return newDashboardAggregationRepositoryWithSQL(sqlDB)
}

func newDashboardAggregationRepositoryWithSQL(sqlq sqlExecutor) *dashboardAggregationRepository {
	return &dashboardAggregationRepository{sql: sqlq}
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*pq.Driver)
	return ok
}

func (r *dashboardAggregationRepository) AggregateRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}
	dailyCoverageStart, dailyCoverageEnd := fullDayCoverageRange(startLocal, endLocal)

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.aggregateRangeInTx(ctx, hourStart, hourEnd, endLocal, dayStart, dayEnd, dailyCoverageStart, dailyCoverageEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateRangeInTx(ctx, hourStart, hourEnd, endLocal, dayStart, dayEnd, dailyCoverageStart, dailyCoverageEnd)
}

// ProcessAccountCostTotals processes one account's bounded usage_logs.id range
// and updates that account's totals and checkpoint in one transaction. New
// usage rows only mark the account pending, so archived accounts with no new
// usage are never scanned again.
func (r *dashboardAggregationRepository) ProcessAccountCostTotals(ctx context.Context, batchSize int64) (int64, error) {
	if r == nil || r.sql == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = 10000
	}

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		processed, err := newDashboardAggregationRepositoryWithSQL(tx).processAccountCostTotalsInTx(ctx, batchSize)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return processed, nil
	}

	processed, err := r.processAccountCostTotalsInTx(ctx, batchSize)
	return processed, err
}

func (r *dashboardAggregationRepository) processAccountCostTotalsInTx(ctx context.Context, batchSize int64) (int64, error) {
	var accountID, lastProcessedID int64
	if err := scanSingleRow(ctx, r.sql, `
		SELECT account_id, last_processed_usage_id
		FROM usage_account_cost_totals
		WHERE needs_processing OR NOT initialized
		ORDER BY computed_at, account_id
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, nil, &accountID, &lastProcessedID); err == sql.ErrNoRows {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	var processedRows int64
	err := scanSingleRow(ctx, r.sql, `
		WITH batch AS MATERIALIZED (
			SELECT
				id,
				total_cost,
				COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) AS account_cost
			FROM usage_logs
			WHERE account_id = $1 AND id > $2
			ORDER BY id
			LIMIT $3
		), totals AS (
			SELECT
				COUNT(*)::BIGINT AS processed_rows,
				COALESCE(MAX(id), $2) AS newest_id,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(total_cost), 0) AS standard_cost
			FROM batch
		), updated AS (
			UPDATE usage_account_cost_totals ledger
			SET total_account_cost = ledger.total_account_cost + totals.account_cost,
				total_standard_account_cost = ledger.total_standard_account_cost + totals.standard_cost,
				published_account_cost = CASE
					WHEN totals.processed_rows < $3 THEN ledger.total_account_cost + totals.account_cost
					ELSE ledger.published_account_cost
				END,
				published_standard_account_cost = CASE
					WHEN totals.processed_rows < $3 THEN ledger.total_standard_account_cost + totals.standard_cost
					ELSE ledger.published_standard_account_cost
				END,
				published_initialized = ledger.published_initialized OR totals.processed_rows < $3,
				last_processed_usage_id = totals.newest_id,
				initialized = ledger.initialized OR totals.processed_rows < $3,
				needs_processing = totals.processed_rows >= $3,
				computed_at = NOW()
			FROM totals
			WHERE ledger.account_id = $1
			RETURNING totals.processed_rows
		)
		SELECT processed_rows FROM updated
	`, []any{accountID, lastProcessedID, batchSize}, &processedRows)
	if err != nil {
		return 0, err
	}
	// 返回“已处理账号数”而非 usage 行数，保证空账号的首次回填也能继续推进
	// 其它账号；无候选账号时上层收到 0 并停止本轮回填。
	return 1, nil
}

func (r *dashboardAggregationRepository) GetAccountCostAggregationState(ctx context.Context) (service.AccountCostAggregationState, error) {
	var state service.AccountCostAggregationState
	if r == nil || r.sql == nil {
		return state, nil
	}
	err := scanSingleRow(ctx, r.sql, `
		SELECT
			COALESCE(MAX(last_processed_usage_id), 0),
			COUNT(*)::BIGINT,
			COUNT(*) FILTER (WHERE needs_processing OR NOT initialized)::BIGINT,
			COALESCE(BOOL_AND(initialized AND NOT needs_processing), TRUE),
			COALESCE(MAX(computed_at), NOW())
		FROM usage_account_cost_totals
	`, nil, &state.LastProcessedUsageID, &state.TotalAccounts, &state.PendingAccounts, &state.BackfillComplete, &state.ComputedAt)
	return state, err
}

func (r *dashboardAggregationRepository) aggregateRangeInTx(ctx context.Context, hourStart, hourEnd, accountCostEnd, dayStart, dayEnd, dailyCoverageStart, dailyCoverageEnd time.Time) error {
	// 以桶边界聚合，允许覆盖 end 所在桶的剩余区间。
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAccountCostAggregates(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyModelAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyUserStats(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAccountCostAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyModelAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyUserStats(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.advanceUserAggregateCoverage(ctx, hourStart, hourEnd, dailyCoverageStart, dailyCoverageEnd); err != nil {
		return err
	}
	if err := r.advanceAccountCostAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	if err := r.advanceModelAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}
	dailyCoverageStart, dailyCoverageEnd := fullDayCoverageRange(startLocal, endLocal)

	// 尽量使用事务保证范围内的一致性（允许在非 *sql.DB 的情况下退化为非事务执行）。
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		accountCostEnd := completedAccountCostBucketEnd(hourEnd)
		if err := txRepo.recomputeRangeInTx(ctx, hourStart, hourEnd, accountCostEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := txRepo.advanceAccountCostAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := txRepo.advanceModelAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := txRepo.advanceUserAggregateCoverage(ctx, hourStart, hourEnd, dailyCoverageStart, dailyCoverageEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	accountCostEnd := completedAccountCostBucketEnd(hourEnd)
	if err := r.recomputeRangeInTx(ctx, hourStart, hourEnd, accountCostEnd, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.advanceAccountCostAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	if err := r.advanceModelAggregateCoverage(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	if err := r.advanceUserAggregateCoverage(ctx, hourStart, hourEnd, dailyCoverageStart, dailyCoverageEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) recomputeRangeInTx(ctx context.Context, hourStart, hourEnd, accountCostEnd, dayStart, dayEnd time.Time) error {
	// 先清空范围内桶，再重建（避免仅增量插入导致活跃用户等指标无法回退）。
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_account_cost_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_user_stats WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_user_stats WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_account_cost_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAccountCostAggregates(ctx, hourStart, accountCostEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyModelAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyUserStats(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAccountCostAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyModelAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertDailyUserStats(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	query := `
		SELECT LEAST(last_aggregated_at, user_hourly_last_aggregated_at)
		FROM usage_dashboard_aggregation_watermark
		WHERE id = 1
	`
	if err := scanSingleRow(ctx, r.sql, query, nil, &ts); err != nil {
		if err == sql.ErrNoRows {
			return time.Unix(0, 0).UTC(), nil
		}
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func (r *dashboardAggregationRepository) GetAccountCostAggregationCoverage(ctx context.Context) (time.Time, time.Time, error) {
	var start, end time.Time
	query := `
		SELECT
			GREATEST(account_cost_hourly_aggregated_from, model_hourly_aggregated_from),
			LEAST(account_cost_hourly_last_aggregated_at, model_hourly_last_aggregated_at)
		FROM usage_dashboard_aggregation_watermark
		WHERE id = 1
	`
	if err := scanSingleRow(ctx, r.sql, query, nil, &start, &end); err != nil {
		if err == sql.ErrNoRows {
			epoch := time.Unix(0, 0).UTC()
			return epoch, epoch, nil
		}
		return time.Time{}, time.Time{}, err
	}
	return start.UTC(), end.UTC(), nil
}

func (r *dashboardAggregationRepository) AggregateAccountCostRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc).Truncate(time.Hour)
	endLocal := end.In(loc).Truncate(time.Hour)
	if !endLocal.After(startLocal) {
		return nil
	}

	run := func(repo *dashboardAggregationRepository) error {
		if _, err := repo.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_account_cost_hourly WHERE bucket_start >= $1 AND bucket_start < $2", startLocal, endLocal); err != nil {
			return err
		}
		if err := repo.upsertHourlyAccountCostAggregates(ctx, startLocal, endLocal); err != nil {
			return err
		}
		if _, err := repo.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_hourly WHERE bucket_start >= $1 AND bucket_start < $2", startLocal, endLocal); err != nil {
			return err
		}
		if err := repo.upsertHourlyModelAggregates(ctx, startLocal, endLocal); err != nil {
			return err
		}
		dayStart := truncateToDay(startLocal)
		dayEnd := truncateToDay(endLocal)
		if endLocal.After(dayEnd) {
			dayEnd = dayEnd.Add(24 * time.Hour)
		}
		if _, err := repo.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_account_cost_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
			return err
		}
		if err := repo.upsertDailyAccountCostAggregates(ctx, dayStart, dayEnd); err != nil {
			return err
		}
		if _, err := repo.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
			return err
		}
		if err := repo.upsertDailyModelAggregates(ctx, dayStart, dayEnd); err != nil {
			return err
		}
		if err := repo.advanceAccountCostAggregateCoverage(ctx, startLocal, endLocal); err != nil {
			return err
		}
		return repo.advanceModelAggregateCoverage(ctx, startLocal, endLocal)
	}
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := run(newDashboardAggregationRepositoryWithSQL(tx)); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return run(r)
}

func completedAccountCostBucketEnd(hourEnd time.Time) time.Time {
	now := timezone.Now()
	if hourEnd.After(now) {
		return now
	}
	return hourEnd
}

func (r *dashboardAggregationRepository) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id)
		DO UPDATE SET
			last_aggregated_at = EXCLUDED.last_aggregated_at,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, aggregatedAt.UTC())
	return err
}

func (r *dashboardAggregationRepository) advanceUserAggregateCoverage(ctx context.Context, hourlyStart, hourlyEnd, dailyStart, dailyEnd time.Time) error {
	if hourlyEnd.After(hourlyStart) {
		if err := r.advanceUserAggregateCoverageColumns(
			ctx,
			"user_hourly_aggregated_from",
			"user_hourly_last_aggregated_at",
			hourlyStart,
			hourlyEnd,
		); err != nil {
			return err
		}
	}
	if dailyEnd.After(dailyStart) {
		if err := r.advanceUserAggregateCoverageColumns(
			ctx,
			"user_daily_aggregated_from",
			"user_daily_last_aggregated_at",
			dailyStart,
			dailyEnd,
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *dashboardAggregationRepository) advanceAccountCostAggregateCoverage(ctx context.Context, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	epoch := time.Unix(0, 0).UTC()
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (
			id,
			last_aggregated_at,
			account_cost_hourly_aggregated_from,
			account_cost_hourly_last_aggregated_at,
			updated_at
		)
		VALUES (1, $3, $1, $2, NOW())
		ON CONFLICT (id)
		DO UPDATE SET
			account_cost_hourly_aggregated_from = CASE
				WHEN usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at <= $3
					THEN EXCLUDED.account_cost_hourly_aggregated_from
				WHEN EXCLUDED.account_cost_hourly_last_aggregated_at < usage_dashboard_aggregation_watermark.account_cost_hourly_aggregated_from
				  OR EXCLUDED.account_cost_hourly_aggregated_from > usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at
					THEN usage_dashboard_aggregation_watermark.account_cost_hourly_aggregated_from
				ELSE LEAST(
					usage_dashboard_aggregation_watermark.account_cost_hourly_aggregated_from,
					EXCLUDED.account_cost_hourly_aggregated_from
				)
			END,
			account_cost_hourly_last_aggregated_at = CASE
				WHEN usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at <= $3
					THEN EXCLUDED.account_cost_hourly_last_aggregated_at
				WHEN EXCLUDED.account_cost_hourly_last_aggregated_at < usage_dashboard_aggregation_watermark.account_cost_hourly_aggregated_from
				  OR EXCLUDED.account_cost_hourly_aggregated_from > usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at
					THEN usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at
				ELSE GREATEST(
					usage_dashboard_aggregation_watermark.account_cost_hourly_last_aggregated_at,
					EXCLUDED.account_cost_hourly_last_aggregated_at
				)
			END,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, start.UTC(), end.UTC(), epoch)
	return err
}

func (r *dashboardAggregationRepository) advanceModelAggregateCoverage(ctx context.Context, start, end time.Time) error {
	return r.advanceUserAggregateCoverageColumns(
		ctx,
		"model_hourly_aggregated_from",
		"model_hourly_last_aggregated_at",
		start,
		end,
	)
}

func (r *dashboardAggregationRepository) advanceUserAggregateCoverageColumns(ctx context.Context, fromColumn, toColumn string, start, end time.Time) error {
	epoch := time.Unix(0, 0).UTC()
	query := fmt.Sprintf(`
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, %s, %s, updated_at)
		VALUES (1, $3, $1, $2, NOW())
		ON CONFLICT (id)
		DO UPDATE SET
			%s = CASE
				WHEN usage_dashboard_aggregation_watermark.%s <= $3 THEN EXCLUDED.%s
				WHEN EXCLUDED.%s < usage_dashboard_aggregation_watermark.%s
				  OR EXCLUDED.%s > usage_dashboard_aggregation_watermark.%s
					THEN usage_dashboard_aggregation_watermark.%s
				ELSE LEAST(usage_dashboard_aggregation_watermark.%s, EXCLUDED.%s)
			END,
			%s = CASE
				WHEN usage_dashboard_aggregation_watermark.%s <= $3 THEN EXCLUDED.%s
				WHEN EXCLUDED.%s < usage_dashboard_aggregation_watermark.%s
				  OR EXCLUDED.%s > usage_dashboard_aggregation_watermark.%s
					THEN usage_dashboard_aggregation_watermark.%s
				ELSE GREATEST(usage_dashboard_aggregation_watermark.%s, EXCLUDED.%s)
			END,
			updated_at = EXCLUDED.updated_at
	`, fromColumn, toColumn,
		fromColumn,
		toColumn, fromColumn,
		toColumn, fromColumn,
		fromColumn, toColumn,
		fromColumn,
		fromColumn, fromColumn,
		toColumn,
		toColumn, toColumn,
		toColumn, fromColumn,
		fromColumn, toColumn,
		toColumn,
		toColumn, toColumn,
	)
	_, err := r.sql.ExecContext(ctx, query, start.UTC(), end.UTC(), epoch)
	return err
}

func (r *dashboardAggregationRepository) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	hourlyCutoffUTC := hourlyCutoff.UTC()
	dailyCutoffUTC := dailyCutoff.UTC()
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_user_stats WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_user_stats WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_model_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if err := r.trimUserAggregateCoverage(ctx, "user_hourly_aggregated_from", "user_hourly_last_aggregated_at", hourlyCutoffUTC); err != nil {
		return err
	}
	if err := r.trimUserAggregateCoverage(ctx, "user_daily_aggregated_from", "user_daily_last_aggregated_at", dailyCutoffUTC); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) trimUserAggregateCoverage(ctx context.Context, fromColumn, toColumn string, cutoff time.Time) error {
	epoch := time.Unix(0, 0).UTC()
	query := fmt.Sprintf(`
		UPDATE usage_dashboard_aggregation_watermark
		SET
			%s = CASE
				WHEN %s <= $1 THEN $2
				WHEN %s < $1 THEN $1
				ELSE %s
			END,
			%s = CASE
				WHEN %s <= $1 THEN $2
				ELSE %s
			END,
			updated_at = NOW()
		WHERE id = 1
	`, fromColumn, toColumn, fromColumn, fromColumn, toColumn, toColumn, toColumn)
	_, err := r.sql.ExecContext(ctx, query, cutoff.UTC(), epoch)
	return err
}

func (r *dashboardAggregationRepository) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	for {
		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid, request_id, api_key_id, request_fingerprint, created_at
				FROM usage_billing_dedup
				WHERE created_at < $1
				LIMIT $2
			), archived AS (
				INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
				SELECT request_id, api_key_id, request_fingerprint, created_at
				FROM victims
				ON CONFLICT (request_id, api_key_id) DO NOTHING
			)
			DELETE FROM usage_billing_dedup
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageBillingDedupCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected < usageBillingDedupCleanupBatchSize {
			return nil
		}
	}
}

func (r *dashboardAggregationRepository) isUsageLogsPartitioned(ctx context.Context) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`
	var partitioned bool
	if err := scanSingleRow(ctx, r.sql, query, nil, &partitioned); err != nil {
		return false, err
	}
	return partitioned, nil
}

func (r *dashboardAggregationRepository) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil || !isPartitioned {
		return err
	}
	monthStart := truncateToMonthUTC(now)
	prevMonth := monthStart.AddDate(0, -1, 0)
	nextMonth := monthStart.AddDate(0, 1, 0)

	for _, m := range []time.Time{prevMonth, monthStart, nextMonth} {
		if err := r.createUsageLogsPartition(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (r *dashboardAggregationRepository) insertHourlyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_hourly_users (bucket_start, user_id)
		SELECT DISTINCT
			date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
			user_id
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) insertDailyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_daily_users (bucket_date, user_id)
		SELECT DISTINCT
			(bucket_start AT TIME ZONE $3)::date AS bucket_date,
			user_id
		FROM usage_dashboard_hourly_users
		WHERE bucket_start >= $1 AND bucket_start < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost,
				COALESCE(SUM(COALESCE(duration_ms, 0)), 0) AS total_duration_ms
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1
		),
		user_counts AS (
			SELECT bucket_start, COUNT(*) AS active_users
			FROM usage_dashboard_hourly_users
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY bucket_start
		)
		INSERT INTO usage_dashboard_hourly (
			bucket_start,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			hourly.bucket_start,
			hourly.total_requests,
			hourly.input_tokens,
			hourly.output_tokens,
			hourly.cache_creation_tokens,
			hourly.cache_read_tokens,
			hourly.total_cost,
			hourly.actual_cost,
			hourly.account_cost,
			hourly.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM hourly
		LEFT JOIN user_counts ON user_counts.bucket_start = hourly.bucket_start
		ON CONFLICT (bucket_start)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyAccountCostAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				account_id,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost,
				COALESCE(SUM(total_cost), 0) AS standard_account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, account_id
		)
		INSERT INTO usage_dashboard_account_cost_hourly (
			bucket_start,
			account_id,
			account_cost,
			standard_account_cost,
			computed_at
		)
		SELECT
			bucket_start,
			account_id,
			account_cost,
			standard_account_cost,
			NOW()
		FROM hourly
		ON CONFLICT (bucket_start, account_id)
		DO UPDATE SET
			account_cost = EXCLUDED.account_cost,
			standard_account_cost = EXCLUDED.standard_account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyAccountCostAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $3)::date AS bucket_date,
				account_id,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(standard_account_cost), 0) AS standard_account_cost
			FROM usage_dashboard_account_cost_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY 1, account_id
		)
		INSERT INTO usage_dashboard_account_cost_daily (
			bucket_date,
			account_id,
			account_cost,
			standard_account_cost,
			computed_at
		)
		SELECT bucket_date, account_id, account_cost, standard_account_cost, NOW()
		FROM daily
		ON CONFLICT (bucket_date, account_id)
		DO UPDATE SET
			account_cost = EXCLUDED.account_cost,
			standard_account_cost = EXCLUDED.standard_account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyModelAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				COALESCE(NULLIF(TRIM(requested_model), ''), NULLIF(TRIM(model), ''), 'unknown') AS model,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, 2
		)
		INSERT INTO usage_dashboard_model_hourly (
			bucket_start, model, total_requests,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, actual_cost, account_cost, computed_at
		)
		SELECT
			bucket_start, model, total_requests,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, actual_cost, account_cost, NOW()
		FROM hourly
		ON CONFLICT (bucket_start, model)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyModelAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $3)::date AS bucket_date,
				model,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost
			FROM usage_dashboard_model_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY 1, model
		)
		INSERT INTO usage_dashboard_model_daily (
			bucket_date, model, total_requests,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, actual_cost, account_cost, computed_at
		)
		SELECT
			bucket_date, model, total_requests,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_cost, actual_cost, account_cost, NOW()
		FROM daily
		ON CONFLICT (bucket_date, model)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) RefreshDashboardCostSnapshot(ctx context.Context, targetStart, targetEnd time.Time) (bool, error) {
	if r == nil || r.sql == nil || !targetEnd.After(targetStart) {
		return false, nil
	}
	today := timezone.Today()
	query := `
		WITH coverage AS (
			SELECT
				account_cost_hourly_aggregated_from AS start_time,
				account_cost_hourly_last_aggregated_at AS end_time
			FROM usage_dashboard_aggregation_watermark
			WHERE id = 1
		),
		ledger_state AS (
			SELECT
				COALESCE(BOOL_AND(published_initialized AND initialized), TRUE) AS complete
			FROM usage_account_cost_totals
		),
		cost_by_account AS (
			SELECT
				account_id,
				published_account_cost AS total_account_cost,
				published_standard_account_cost AS total_standard_account_cost
			FROM usage_account_cost_totals
		),
		today_by_account AS (
			SELECT
				account_id,
				COALESCE(SUM(account_cost), 0) AS today_account_cost,
				COALESCE(SUM(standard_account_cost), 0) AS today_standard_account_cost
			FROM usage_dashboard_account_cost_daily
			WHERE bucket_date = $3::date
			GROUP BY account_id
		),
		costed_accounts AS (
			SELECT
				a.id,
				a.platform,
				a.total_cost_cny,
				COALESCE(c.total_account_cost, 0) AS total_account_cost,
				COALESCE(c.total_standard_account_cost, 0) AS total_standard_account_cost,
				COALESCE(t.today_account_cost, 0) AS today_account_cost,
				COALESCE(t.today_standard_account_cost, 0) AS today_standard_account_cost
			FROM accounts a
			LEFT JOIN cost_by_account c ON c.account_id = a.id
			LEFT JOIN today_by_account t ON t.account_id = a.id
			WHERE a.deleted_at IS NULL AND a.total_cost_cny > 0
		),
		metrics AS (
			SELECT
				COALESCE((SELECT SUM(total_cost_cny) FROM costed_accounts), 0) AS total_cost_cny,
				COALESCE((SELECT SUM(total_account_cost) FROM cost_by_account), 0) AS total_account_cost,
				COALESCE((SELECT SUM(today_account_cost) FROM today_by_account), 0) AS today_account_cost,
				COALESCE((SELECT SUM(total_standard_account_cost) FROM costed_accounts), 0) AS costed_total_standard_account_cost,
				COALESCE((SELECT SUM(total_cost_cny) FROM costed_accounts WHERE platform = $4), 0) AS anthropic_cost_cny,
				COALESCE((SELECT SUM(total_standard_account_cost) FROM costed_accounts WHERE platform = $4), 0) AS anthropic_standard_account_cost,
				COALESCE((SELECT SUM(total_cost_cny) FROM costed_accounts WHERE platform = $5), 0) AS openai_cost_cny,
				COALESCE((SELECT SUM(total_standard_account_cost) FROM costed_accounts WHERE platform = $5), 0) AS openai_standard_account_cost,
				COALESCE((
					SELECT SUM(today_standard_account_cost * total_cost_cny / NULLIF(total_standard_account_cost, 0))
					FROM costed_accounts
					WHERE total_standard_account_cost > 0
				), 0) AS today_real_cost_cny
		)
		INSERT INTO usage_dashboard_cost_snapshot (
			id, today_real_cost_cny, total_cost_cny, total_account_cost, today_account_cost,
			average_cost_cny_per_usd, anthropic_cost_cny_per_usd, openai_cost_cny_per_usd,
			coverage_start, coverage_end, aggregation_complete, computed_at
		)
		SELECT
			1,
			m.today_real_cost_cny,
			m.total_cost_cny,
			m.total_account_cost,
			m.today_account_cost,
			CASE WHEN m.costed_total_standard_account_cost > 0 THEN m.total_cost_cny / m.costed_total_standard_account_cost ELSE 0 END,
			CASE WHEN m.anthropic_standard_account_cost > 0 THEN m.anthropic_cost_cny / m.anthropic_standard_account_cost ELSE 0 END,
			CASE WHEN m.openai_standard_account_cost > 0 THEN m.openai_cost_cny / m.openai_standard_account_cost ELSE 0 END,
			c.start_time,
			c.end_time,
			l.complete,
			NOW()
		FROM coverage c
		CROSS JOIN ledger_state l
		CROSS JOIN metrics m
		WHERE c.start_time <= $1
			AND c.end_time >= $2
			AND l.complete
		ON CONFLICT (id)
		DO UPDATE SET
			today_real_cost_cny = EXCLUDED.today_real_cost_cny,
			total_cost_cny = EXCLUDED.total_cost_cny,
			total_account_cost = EXCLUDED.total_account_cost,
			today_account_cost = EXCLUDED.today_account_cost,
			average_cost_cny_per_usd = EXCLUDED.average_cost_cny_per_usd,
			anthropic_cost_cny_per_usd = EXCLUDED.anthropic_cost_cny_per_usd,
			openai_cost_cny_per_usd = EXCLUDED.openai_cost_cny_per_usd,
			coverage_start = EXCLUDED.coverage_start,
			coverage_end = EXCLUDED.coverage_end,
			aggregation_complete = EXCLUDED.aggregation_complete,
			computed_at = EXCLUDED.computed_at
		RETURNING aggregation_complete
	`
	var complete bool
	err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{targetStart.UTC(), targetEnd.UTC(), today, service.PlatformAnthropic, service.PlatformOpenAI},
		&complete,
	)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return complete, err
}

// MarkDashboardCostSnapshotStale prevents a pre-cleanup snapshot from being
// presented as complete while the affected usage range is being recomputed.
func (r *dashboardAggregationRepository) MarkDashboardCostSnapshotStale(ctx context.Context) error {
	if r == nil || r.sql == nil {
		return nil
	}
	_, err := r.sql.ExecContext(ctx, `
		UPDATE usage_dashboard_cost_snapshot
		SET aggregation_complete = FALSE
		WHERE id = 1
	`)
	if isUndefinedTableError(err) {
		return nil
	}
	return err
}

func (r *dashboardAggregationRepository) GetDashboardCostSummary(ctx context.Context) (*usagestats.DashboardCostSummary, error) {
	query := `
		SELECT
			today_real_cost_cny,
			total_cost_cny,
			total_account_cost,
			today_account_cost,
			average_cost_cny_per_usd,
			anthropic_cost_cny_per_usd,
			openai_cost_cny_per_usd,
			coverage_start,
			coverage_end,
			aggregation_complete,
			computed_at
		FROM usage_dashboard_cost_snapshot
		WHERE id = 1
	`
	result := &usagestats.DashboardCostSummary{}
	var coverageStart, coverageEnd, computedAt time.Time
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		nil,
		&result.TodayRealCostCNY,
		&result.TotalCostCNY,
		&result.TotalAccountCost,
		&result.TodayAccountCost,
		&result.AverageCostCNYPerUSD,
		&result.AnthropicCostCNYPerUSD,
		&result.OpenAICostCNYPerUSD,
		&coverageStart,
		&coverageEnd,
		&result.AggregationComplete,
		&computedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	result.AsOf = computedAt.UTC().Format(time.RFC3339)
	result.CoverageStart = coverageStart.UTC().Format(time.RFC3339)
	result.CoverageEnd = coverageEnd.UTC().Format(time.RFC3339)
	return result, nil
}

func (r *dashboardAggregationRepository) upsertDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $5)::date AS bucket_date,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(total_duration_ms), 0) AS total_duration_ms
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $5)::date
		),
		user_counts AS (
			SELECT bucket_date, COUNT(*) AS active_users
			FROM usage_dashboard_daily_users
			WHERE bucket_date >= $3::date AND bucket_date < $4::date
			GROUP BY bucket_date
		)
		INSERT INTO usage_dashboard_daily (
			bucket_date,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			active_users,
			computed_at
		)
		SELECT
			daily.bucket_date,
			daily.total_requests,
			daily.input_tokens,
			daily.output_tokens,
			daily.cache_creation_tokens,
			daily.cache_read_tokens,
			daily.total_cost,
			daily.actual_cost,
			daily.account_cost,
			daily.total_duration_ms,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM daily
		LEFT JOIN user_counts ON user_counts.bucket_date = daily.bucket_date
		ON CONFLICT (bucket_date)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyUserStats(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				user_id,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1, user_id
		)
		INSERT INTO usage_dashboard_hourly_user_stats (
			bucket_start,
			user_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		SELECT
			bucket_start,
			user_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			NOW()
		FROM hourly
		ON CONFLICT (bucket_start, user_id)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyUserStats(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $3)::date AS bucket_date,
				user_id,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost
			FROM usage_dashboard_hourly_user_stats
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $3)::date, user_id
		)
		INSERT INTO usage_dashboard_daily_user_stats (
			bucket_date,
			user_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		SELECT
			bucket_date,
			user_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			NOW()
		FROM daily
		ON CONFLICT (bucket_date, user_id)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) createUsageLogsPartition(ctx context.Context, month time.Time) error {
	monthStart := truncateToMonthUTC(month)
	nextMonth := monthStart.AddDate(0, 1, 0)
	name := fmt.Sprintf("usage_logs_%s", monthStart.Format("200601"))
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF usage_logs FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(name),
		pq.QuoteLiteral(monthStart.Format("2006-01-02")),
		pq.QuoteLiteral(nextMonth.Format("2006-01-02")),
	)
	_, err := r.sql.ExecContext(ctx, query)
	return err
}

func truncateToDay(t time.Time) time.Time {
	return timezone.StartOfDay(t)
}

func fullDayCoverageRange(start, end time.Time) (time.Time, time.Time) {
	dailyStart := truncateToDay(start)
	if start.After(dailyStart) {
		dailyStart = dailyStart.Add(24 * time.Hour)
	}
	dailyEnd := truncateToDay(end)
	if !dailyEnd.After(dailyStart) {
		return dailyStart, dailyStart
	}
	return dailyStart, dailyEnd
}

func truncateToMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
