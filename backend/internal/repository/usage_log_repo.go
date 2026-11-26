package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	gocache "github.com/patrickmn/go-cache"
)

const rawUsageLogModelColumn = "model"

// rawUsageLogModelColumn preserves the exact stored usage_logs.model semantics for direct filters.
// Historical rows may contain upstream/billing model values, while newer rows store requested_model.
// Requested/upstream/mapping analytics must use resolveModelDimensionExpression instead.

// usageLogSuccessFilterUL 用于把"失败请求 usage log"（tokens=0、cost=0、不计费的占位记录）
// 从统计性聚合中排除，避免污染 Dashboard / 用量拆分等指标。
//
// succeeded 是新写入的明确结果；历史行保持 NULL 并回退 actual_cost，以免迁移
// 篡改既有报表。零价/免费成功请求因此不会被统计为失败。
// 配合 `FROM usage_logs ul` JOIN 查询使用。
const usageLogSuccessFilterUL = "COALESCE(ul.succeeded, ul.actual_cost > 0)"

// usageLogEffectivePlatformExpr 用于按"有效平台"维度聚合 usage_logs：
// 优先取请求实际走的分组 platform，若分组未设置 platform 再 fallback 到 account.platform。
// Composite groups are a routing layer, so platform analytics must use the
// resolved concrete account platform instead of grouping spend under "composite".
// 配套要求查询里 LEFT JOIN groups g ON g.id = ul.group_id 与 LEFT JOIN accounts a ON a.id = ul.account_id。
const usageLogEffectivePlatformExpr = "CASE WHEN g.platform = 'composite' THEN a.platform ELSE COALESCE(NULLIF(g.platform,''), a.platform) END"

// account_stats_cost is the resolved upstream/account cost when present.
// Legacy rows without that snapshot use the recorded standard cost directly.
const usageAccountCostSumExpr = "COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost)), 0)"

// dateFormatWhitelist 将 granularity 参数映射为 PostgreSQL TO_CHAR 格式字符串，防止外部输入直接拼入 SQL
var dateFormatWhitelist = map[string]string{
	"hour":  "YYYY-MM-DD HH24:00",
	"day":   "YYYY-MM-DD",
	"week":  "IYYY-IW",
	"month": "YYYY-MM",
}

// safeDateFormat 根据白名单获取 dateFormat，未匹配时返回默认值
func safeDateFormat(granularity string) string {
	if f, ok := dateFormatWhitelist[granularity]; ok {
		return f
	}
	return "YYYY-MM-DD"
}

func appendUsageLogBillingModeWhereCondition(conditions []string, args []any, billingMode string) ([]string, []any) {
	return appendUsageLogBillingModeWhereConditionWithAlias(conditions, args, billingMode, "")
}

func appendUsageLogBillingModeWhereConditionWithAlias(conditions []string, args []any, billingMode string, alias string) ([]string, []any) {
	mode := strings.TrimSpace(billingMode)
	if mode == "" {
		return conditions, args
	}
	placeholder := fmt.Sprintf("$%d", len(args)+1)
	billingModeColumn := usageLogQualifiedColumn(alias, "billing_mode")
	imageCountColumn := usageLogQualifiedColumn(alias, "image_count")
	switch service.BillingMode(mode) {
	case service.BillingModeImage:
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR ((%s IS NULL OR %s = '') AND COALESCE(%s, 0) > 0))", billingModeColumn, placeholder, billingModeColumn, billingModeColumn, imageCountColumn))
	case service.BillingModeToken:
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR ((%s IS NULL OR %s = '') AND COALESCE(%s, 0) <= 0))", billingModeColumn, placeholder, billingModeColumn, billingModeColumn, imageCountColumn))
	default:
		conditions = append(conditions, fmt.Sprintf("%s = %s", billingModeColumn, placeholder))
	}
	args = append(args, mode)
	return conditions, args
}

func appendUsageLogBillingModeQueryFilter(query string, args []any, billingMode string, alias string) (string, []any) {
	conditions, nextArgs := appendUsageLogBillingModeWhereConditionWithAlias(nil, args, billingMode, alias)
	if len(conditions) == 0 {
		return query, nextArgs
	}
	return query + " AND " + conditions[0], nextArgs
}

func appendUsageLogUpstreamModelMismatchWhereCondition(
	conditions []string,
	upstreamModelMismatch *bool,
	alias string,
) []string {
	if upstreamModelMismatch == nil {
		return conditions
	}
	column := usageLogQualifiedColumn(alias, "upstream_model_mismatch")
	if *upstreamModelMismatch {
		return append(conditions, column+" IS TRUE")
	}
	return append(conditions, column+" IS FALSE")
}

func appendUsageLogUpstreamModelMismatchQueryFilter(query string, upstreamModelMismatch *bool, alias string) string {
	conditions := appendUsageLogUpstreamModelMismatchWhereCondition(nil, upstreamModelMismatch, alias)
	if len(conditions) == 0 {
		return query
	}
	return query + " AND " + conditions[0]
}

func usageLogQualifiedColumn(alias string, column string) string {
	if strings.TrimSpace(alias) == "" {
		return column
	}
	return strings.TrimSpace(alias) + "." + column
}

func usageLogModelDimensionExpression(source string, alias string) string {
	requestedModel := usageLogQualifiedColumn(alias, "requested_model")
	model := usageLogQualifiedColumn(alias, "model")
	upstreamModel := usageLogQualifiedColumn(alias, "upstream_model")
	requestedExpr := fmt.Sprintf("COALESCE(NULLIF(TRIM(%s), ''), %s)", requestedModel, model)
	upstreamExpr := fmt.Sprintf("COALESCE(NULLIF(TRIM(%s), ''), %s)", upstreamModel, model)
	switch usagestats.NormalizeModelSource(source) {
	case usagestats.ModelSourceUpstream:
		return upstreamExpr
	case usagestats.ModelSourceMapping:
		return fmt.Sprintf("(%s || ' -> ' || %s)", requestedExpr, upstreamExpr)
	default:
		return requestedExpr
	}
}

func appendUsageLogModelQueryFilter(query string, args []any, model string, source string, alias string) (string, []any) {
	if strings.TrimSpace(model) == "" {
		return query, args
	}
	query += fmt.Sprintf(" AND %s = $%d", usageLogModelDimensionExpression(source, alias), len(args)+1)
	args = append(args, model)
	return query, args
}

// appendRawUsageLogModelQueryFilter keeps direct model filters on the raw model column for backward
// compatibility with historical rows. Requested/upstream analytics must use
// resolveModelDimensionExpression instead.
func appendRawUsageLogModelQueryFilter(query string, args []any, model string) (string, []any) {
	if strings.TrimSpace(model) == "" {
		return query, args
	}
	query += fmt.Sprintf(" AND %s = $%d", rawUsageLogModelColumn, len(args)+1)
	args = append(args, model)
	return query, args
}

type usageLogRepository struct {
	client *dbent.Client
	sql    sqlExecutor
	db     *sql.DB

	createBatchOnce     sync.Once
	createBatchCh       chan usageLogCreateRequest
	bestEffortBatchOnce sync.Once
	bestEffortBatchCh   chan usageLogBestEffortRequest
	bestEffortRecent    *gocache.Cache
}

func NewUsageLogRepository(client *dbent.Client, sqlDB *sql.DB) service.UsageLogRepository {
	return newUsageLogRepositoryWithSQL(client, sqlDB)
}

func newUsageLogRepositoryWithSQL(client *dbent.Client, sqlq sqlExecutor) *usageLogRepository {
	// 使用 scanSingleRow 替代 QueryRowContext，保证 ent.Tx 作为 sqlExecutor 可用。
	repo := &usageLogRepository{client: client, sql: sqlq}
	if db, ok := sqlq.(*sql.DB); ok {
		repo.db = db
	}
	repo.bestEffortRecent = gocache.New(usageLogBestEffortRecentTTL, time.Minute)
	return repo
}

// getPerformanceStats 获取 RPM 和 TPM（近5分钟平均值，可选按用户过滤）
func (r *usageLogRepository) getPerformanceStats(ctx context.Context, userID int64) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens), 0) as token_count
		FROM usage_logs
		WHERE created_at >= $1`
	args := []any{fiveMinutesAgo}
	if userID > 0 {
		query += " AND user_id = $2"
		args = append(args, userID)
	}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	return requestCount / 5, tokenCount / 5, nil
}

// UserStats 用户使用统计
type UserStats struct {
	TotalRequests   int64   `json:"total_requests"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCost       float64 `json:"total_cost"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
}

func (r *usageLogRepository) GetUserStats(ctx context.Context, userID int64, startTime, endTime time.Time) (*UserStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(actual_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`

	stats := &UserStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{userID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.TotalCost,
		&stats.InputTokens,
		&stats.OutputTokens,
		&stats.CacheReadTokens,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// DashboardStats 仪表盘统计
type DashboardStats = usagestats.DashboardStats

// fillDashboardCostCNYStats is retained for legacy integration fixtures. Cost
// snapshots are now populated by dashboard aggregation and exposed directly.
//
//nolint:unused // 保留旧集成测试与外部适配代码的兼容入口。
func (r *usageLogRepository) fillDashboardCostCNYStats(ctx context.Context, stats *DashboardStats, todayStart time.Time) error {
	_ = ctx
	_ = todayStart
	if stats == nil {
		return nil
	}
	return nil
}

func (r *usageLogRepository) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{}
	now := timezone.Now()
	todayStart := timezone.Today()

	if err := r.fillDashboardEntityStats(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}
	if err := r.fillDashboardUsageStatsAggregated(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}

	rpm, tpm, err := r.getPerformanceStats(ctx, 0)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

func (r *usageLogRepository) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return nil, errors.New("统计时间范围无效")
	}

	stats := &DashboardStats{}
	now := timezone.Now()
	todayStart := timezone.Today()

	if err := r.fillDashboardEntityStats(ctx, stats, todayStart, now); err != nil {
		return nil, err
	}
	if err := r.fillDashboardUsageStatsFromUsageLogs(ctx, stats, startUTC, endUTC, todayStart, now); err != nil {
		return nil, err
	}

	rpm, tpm, err := r.getPerformanceStats(ctx, 0)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

func (r *usageLogRepository) fillDashboardEntityStats(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	userStatsQuery := `
		SELECT
			COUNT(*) as total_users,
			COUNT(CASE WHEN created_at >= $1 THEN 1 END) as today_new_users
		FROM users
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		userStatsQuery,
		[]any{todayUTC},
		&stats.TotalUsers,
		&stats.TodayNewUsers,
	); err != nil {
		return err
	}

	apiKeyStatsQuery := `
		SELECT
			COUNT(*) as total_api_keys,
			COUNT(CASE WHEN status = $1 THEN 1 END) as active_api_keys
		FROM api_keys
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		apiKeyStatsQuery,
		[]any{service.StatusActive},
		&stats.TotalAPIKeys,
		&stats.ActiveAPIKeys,
	); err != nil {
		return err
	}

	accountStatsQuery := `
		SELECT
			COUNT(*) as total_accounts,
			COUNT(CASE WHEN status = $1 AND schedulable = true THEN 1 END) as normal_accounts,
			COUNT(CASE WHEN status = $2 THEN 1 END) as error_accounts,
			COUNT(CASE WHEN rate_limited_at IS NOT NULL AND rate_limit_reset_at > $3 THEN 1 END) as ratelimit_accounts,
			COUNT(CASE WHEN overload_until IS NOT NULL AND overload_until > $4 THEN 1 END) as overload_accounts
		FROM accounts
		WHERE deleted_at IS NULL
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		accountStatsQuery,
		[]any{service.StatusActive, service.StatusError, now, now},
		&stats.TotalAccounts,
		&stats.NormalAccounts,
		&stats.ErrorAccounts,
		&stats.RateLimitAccounts,
		&stats.OverloadAccounts,
	); err != nil {
		return err
	}
	return nil
}

func (r *usageLogRepository) fillDashboardUsageStatsAggregated(ctx context.Context, stats *DashboardStats, todayUTC, now time.Time) error {
	totalStatsQuery := `
		SELECT
			COALESCE(SUM(total_requests), 0) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(SUM(total_duration_ms), 0) as total_duration_ms
		FROM usage_dashboard_daily
	`
	var totalDurationMs int64
	if err := scanSingleRow(
		ctx,
		r.sql,
		totalStatsQuery,
		nil,
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&totalDurationMs,
	); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	if stats.TotalRequests > 0 {
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	todayStatsQuery := `
		SELECT
			total_requests as today_requests,
			input_tokens as today_input_tokens,
			output_tokens as today_output_tokens,
			cache_creation_tokens as today_cache_creation_tokens,
			cache_read_tokens as today_cache_read_tokens,
			total_cost as today_cost,
			actual_cost as today_actual_cost,
			active_users as active_users
		FROM usage_dashboard_daily
		WHERE bucket_date = $1::date
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		todayStatsQuery,
		[]any{todayUTC},
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
		&stats.ActiveUsers,
	); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
	}
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens
	if err := r.fillDashboardAccountCostFromAggregates(ctx, stats, todayUTC); err != nil {
		return err
	}
	hourlyActiveQuery := `
		SELECT active_users
		FROM usage_dashboard_hourly
		WHERE bucket_start = $1
	`
	hourStart := now.In(timezone.Location()).Truncate(time.Hour)
	if err := scanSingleRow(ctx, r.sql, hourlyActiveQuery, []any{hourStart}, &stats.HourlyActiveUsers); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
	}

	return nil
}

func (r *usageLogRepository) fillDashboardAccountCostFromAggregates(ctx context.Context, stats *DashboardStats, todayUTC time.Time) error {
	query := `
		SELECT
			COALESCE(SUM(account_cost), 0) AS total_account_cost,
			COALESCE(SUM(account_cost) FILTER (WHERE bucket_date = $1::date), 0) AS today_account_cost,
			COALESCE(SUM(api_key_actual_cost), 0) AS api_key_actual_cost,
			COALESCE(SUM(api_key_account_cost), 0) AS api_key_account_cost,
			COALESCE(SUM(api_key_actual_cost) FILTER (WHERE bucket_date = $1::date), 0) AS today_api_key_actual_cost,
			COALESCE(SUM(api_key_account_cost) FILTER (WHERE bucket_date = $1::date), 0) AS today_api_key_account_cost
		FROM usage_dashboard_daily
	`
	var totalAPIKeyActual, totalAPIKeyAccount, todayAPIKeyActual, todayAPIKeyAccount float64
	if err := scanSingleRow(ctx, r.sql, query, []any{todayUTC}, &stats.TotalAccountCost, &stats.TodayAccountCost, &totalAPIKeyActual, &totalAPIKeyAccount, &todayAPIKeyActual, &todayAPIKeyAccount); err != nil {
		return err
	}
	stats.TotalAPIKeyProfit = totalAPIKeyActual - totalAPIKeyAccount
	stats.TodayAPIKeyProfit = todayAPIKeyActual - todayAPIKeyAccount
	if totalAPIKeyActual > 0 {
		stats.TotalAPIKeyProfitRate = stats.TotalAPIKeyProfit / totalAPIKeyActual
	}
	if todayAPIKeyActual > 0 {
		stats.TodayAPIKeyProfitRate = stats.TodayAPIKeyProfit / todayAPIKeyActual
	}
	return nil
}

func isUndefinedColumnError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr == nil {
		return false
	}
	return string(pqErr.Code) == "42703"
}

func isUndefinedTableError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr == nil {
		return false
	}
	return string(pqErr.Code) == "42P01"
}

func (r *usageLogRepository) fillDashboardUsageStatsFromUsageLogs(ctx context.Context, stats *DashboardStats, startUTC, endUTC, todayUTC, now time.Time) error {
	todayEnd := todayUTC.Add(24 * time.Hour)
	combinedStatsQuery := `
		WITH scoped AS (
			SELECT
				created_at,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				total_cost,
				actual_cost,
				COALESCE(account_stats_cost, total_cost) AS account_cost,
				COALESCE(duration_ms, 0) AS duration_ms
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(*) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz) AS total_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_actual_cost,
			COALESCE(SUM(account_cost) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_account_cost,
			COALESCE(SUM(duration_ms) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_duration_ms,
			COUNT(*) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz) AS today_requests,
			COALESCE(SUM(input_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_input_tokens,
			COALESCE(SUM(output_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_output_tokens,
			COALESCE(SUM(cache_creation_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cache_read_tokens,
			COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_actual_cost,
			COALESCE(SUM(account_cost) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_account_cost
		FROM scoped
	`
	var totalDurationMs int64
	if err := scanSingleRow(
		ctx,
		r.sql,
		combinedStatsQuery,
		[]any{startUTC, endUTC, todayUTC, todayEnd},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.TotalAccountCost,
		&totalDurationMs,
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
		&stats.TodayAccountCost,
	); err != nil {
		return err
	}
	if err := r.fillDashboardAPIKeyProfitFromUsageLogs(ctx, stats, startUTC, endUTC, todayUTC, todayEnd); err != nil {
		return err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens
	if stats.TotalRequests > 0 {
		stats.AverageDurationMs = float64(totalDurationMs) / float64(stats.TotalRequests)
	}

	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	hourStart := now.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	activeUsersQuery := `
		WITH scoped AS (
			SELECT user_id, created_at
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COUNT(DISTINCT CASE WHEN created_at >= $1::timestamptz AND created_at < $2::timestamptz THEN user_id END) AS active_users,
			COUNT(DISTINCT CASE WHEN created_at >= $3::timestamptz AND created_at < $4::timestamptz THEN user_id END) AS hourly_active_users
		FROM scoped
	`
	if err := scanSingleRow(ctx, r.sql, activeUsersQuery, []any{todayUTC, todayEnd, hourStart, hourEnd}, &stats.ActiveUsers, &stats.HourlyActiveUsers); err != nil {
		return err
	}

	return nil
}

func (r *usageLogRepository) fillDashboardAPIKeyProfitFromUsageLogs(ctx context.Context, stats *DashboardStats, startUTC, endUTC, todayUTC, todayEnd time.Time) error {
	query := `
		SELECT
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $1 AND ul.created_at < $2), 0),
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost)) FILTER (WHERE ul.created_at >= $1 AND ul.created_at < $2), 0),
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $3 AND ul.created_at < $4), 0),
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost)) FILTER (WHERE ul.created_at >= $3 AND ul.created_at < $4), 0)
		FROM usage_logs ul
		JOIN accounts a ON a.id = ul.account_id AND a.type = 'apikey'
		WHERE TRUE
		  AND ul.created_at >= LEAST($1, $3) AND ul.created_at < GREATEST($2, $4)
		  AND COALESCE(ul.succeeded, ul.actual_cost > 0)
	`
	var charged, accountCost, todayCharged, todayAccountCost float64
	if err := scanSingleRow(ctx, r.sql, query, []any{startUTC, endUTC, todayUTC, todayEnd}, &charged, &accountCost, &todayCharged, &todayAccountCost); err != nil {
		return err
	}
	stats.TotalAPIKeyProfit = charged - accountCost
	stats.TodayAPIKeyProfit = todayCharged - todayAccountCost
	if charged > 0 {
		stats.TotalAPIKeyProfitRate = stats.TotalAPIKeyProfit / charged
	}
	if todayCharged > 0 {
		stats.TodayAPIKeyProfitRate = stats.TodayAPIKeyProfit / todayCharged
	}
	return nil
}

func (r *usageLogRepository) getUserUsageTrendFromAggregates(ctx context.Context, startTime, endTime time.Time, granularity string, limit int, sourceKind dashboardUserAggregateCoverageKind, metric string) ([]UserUsageTrendPoint, bool, error) {
	if limit <= 0 {
		limit = 12
	}

	dateFormat := safeDateFormat(granularity)
	rankExpr := "SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens)"
	if metric == "actual_cost" {
		rankExpr = "SUM(actual_cost)"
	}
	query := ""
	args := []any{startTime, endTime, limit, startTime, endTime}
	switch sourceKind {
	case dashboardUserAggregateCoverageHourly:
		args = append(args, startTime.Location().String())
		query = fmt.Sprintf(`
			WITH top_users AS (
				SELECT user_id
				FROM usage_dashboard_hourly_user_stats
				WHERE bucket_start >= $1 AND bucket_start < $2
				GROUP BY user_id
				ORDER BY %s DESC
				LIMIT $3
			)
			SELECT
				TO_CHAR(s.bucket_start AT TIME ZONE $6, '%s') as date,
				s.user_id,
				COALESCE(u.email, '') as email,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(s.total_requests), 0) as requests,
				COALESCE(SUM(s.input_tokens + s.output_tokens + s.cache_creation_tokens + s.cache_read_tokens), 0) as tokens,
				COALESCE(SUM(s.total_cost), 0) as cost,
				COALESCE(SUM(s.actual_cost), 0) as actual_cost
			FROM usage_dashboard_hourly_user_stats s
			LEFT JOIN users u ON u.id = s.user_id
			WHERE s.user_id IN (SELECT user_id FROM top_users)
			  AND s.bucket_start >= $4 AND s.bucket_start < $5
			GROUP BY date, s.user_id, u.email, u.username
			ORDER BY date ASC, tokens DESC
		`, rankExpr, dateFormat)
	case dashboardUserAggregateCoverageDaily:
		query = fmt.Sprintf(`
			WITH top_users AS (
				SELECT user_id
				FROM usage_dashboard_daily_user_stats
				WHERE bucket_date >= $1::date AND bucket_date < $2::date
				GROUP BY user_id
				ORDER BY %s DESC
				LIMIT $3
			)
			SELECT
				TO_CHAR(s.bucket_date::timestamp, '%s') as date,
				s.user_id,
				COALESCE(u.email, '') as email,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(s.total_requests), 0) as requests,
				COALESCE(SUM(s.input_tokens + s.output_tokens + s.cache_creation_tokens + s.cache_read_tokens), 0) as tokens,
				COALESCE(SUM(s.total_cost), 0) as cost,
				COALESCE(SUM(s.actual_cost), 0) as actual_cost
			FROM usage_dashboard_daily_user_stats s
			LEFT JOIN users u ON u.id = s.user_id
			WHERE s.user_id IN (SELECT user_id FROM top_users)
			  AND s.bucket_date >= $4::date AND s.bucket_date < $5::date
			GROUP BY date, s.user_id, u.email, u.username
			ORDER BY date ASC, tokens DESC
		`, rankExpr, dateFormat)
	default:
		return nil, false, nil
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	results := make([]UserUsageTrendPoint, 0)
	for rows.Next() {
		var row UserUsageTrendPoint
		if err = rows.Scan(&row.Date, &row.UserID, &row.Email, &row.Username, &row.Requests, &row.Tokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	return results, len(results) > 0, nil
}

func (r *usageLogRepository) getUserSpendingRankingFromAggregates(ctx context.Context, startTime, endTime time.Time, limit int) (*UserSpendingRankingResponse, bool, error) {
	tableName := "usage_dashboard_hourly_user_stats"
	bucketWhere := "bucket_start >= $1 AND bucket_start < $2"
	if isWholeDashboardDayRange(startTime, endTime) {
		tableName = "usage_dashboard_daily_user_stats"
		bucketWhere = "bucket_date >= $1::date AND bucket_date < $2::date"
	}

	query := fmt.Sprintf(`
		WITH user_spend AS (
			SELECT
				s.user_id,
				COALESCE(u.email, '') as email,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(s.actual_cost), 0) as actual_cost,
				COALESCE(SUM(s.total_requests), 0) as requests,
				COALESCE(SUM(s.input_tokens + s.output_tokens + s.cache_creation_tokens + s.cache_read_tokens), 0) as tokens
			FROM %s s
			LEFT JOIN users u ON u.id = s.user_id
			WHERE %s
			GROUP BY s.user_id, u.email, u.username
		),
		ranked AS (
			SELECT
				user_id,
				email,
				username,
				actual_cost,
				requests,
				tokens,
				COALESCE(SUM(actual_cost) OVER (), 0) as total_actual_cost,
				COALESCE(SUM(requests) OVER (), 0) as total_requests,
				COALESCE(SUM(tokens) OVER (), 0) as total_tokens
			FROM user_spend
			ORDER BY actual_cost DESC, tokens DESC, user_id ASC
			LIMIT $3
		)
		SELECT
			user_id,
			email,
			username,
			actual_cost,
			requests,
			tokens,
			total_actual_cost,
			total_requests,
			total_tokens
		FROM ranked
		ORDER BY actual_cost DESC, tokens DESC, user_id ASC
	`, tableName, bucketWhere)

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	ranking, totalActualCost, totalRequests, totalTokens, err := scanUserSpendingRankingRows(rows)
	if err != nil {
		return nil, false, err
	}
	return &UserSpendingRankingResponse{
		Ranking:         ranking,
		TotalActualCost: totalActualCost,
		TotalRequests:   totalRequests,
		TotalTokens:     totalTokens,
	}, len(ranking) > 0, nil
}

func (r *usageLogRepository) getUserSpendingRankingFromHourlyAggregates(ctx context.Context, startTime, endTime time.Time, limit int) (*UserSpendingRankingResponse, bool, error) {
	query := `
		WITH user_spend AS (
			SELECT
				s.user_id,
				COALESCE(u.email, '') as email,
				COALESCE(u.username, '') as username,
				COALESCE(SUM(s.actual_cost), 0) as actual_cost,
				COALESCE(SUM(s.total_requests), 0) as requests,
				COALESCE(SUM(s.input_tokens + s.output_tokens + s.cache_creation_tokens + s.cache_read_tokens), 0) as tokens
			FROM usage_dashboard_hourly_user_stats s
			LEFT JOIN users u ON u.id = s.user_id
			WHERE s.bucket_start >= $1 AND s.bucket_start < $2
			GROUP BY s.user_id, u.email, u.username
		),
		ranked AS (
			SELECT
				user_id,
				email,
				username,
				actual_cost,
				requests,
				tokens,
				COALESCE(SUM(actual_cost) OVER (), 0) as total_actual_cost,
				COALESCE(SUM(requests) OVER (), 0) as total_requests,
				COALESCE(SUM(tokens) OVER (), 0) as total_tokens
			FROM user_spend
			ORDER BY actual_cost DESC, tokens DESC, user_id ASC
			LIMIT $3
		)
		SELECT
			user_id,
			email,
			username,
			actual_cost,
			requests,
			tokens,
			total_actual_cost,
			total_requests,
			total_tokens
		FROM ranked
		ORDER BY actual_cost DESC, tokens DESC, user_id ASC
	`

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	ranking, totalActualCost, totalRequests, totalTokens, err := scanUserSpendingRankingRows(rows)
	if err != nil {
		return nil, false, err
	}
	return &UserSpendingRankingResponse{
		Ranking:         ranking,
		TotalActualCost: totalActualCost,
		TotalRequests:   totalRequests,
		TotalTokens:     totalTokens,
	}, len(ranking) > 0, nil
}

func isWholeDashboardDayRange(startTime, endTime time.Time) bool {
	if !endTime.After(startTime) {
		return false
	}
	loc := timezone.Location()
	startLocal := startTime.In(loc)
	endLocal := endTime.In(loc)
	return startLocal.Equal(timezone.StartOfDay(startLocal)) && endLocal.Equal(timezone.StartOfDay(endLocal))
}

type dashboardUserAggregateCoverageKind int

const (
	dashboardUserAggregateCoverageHourly dashboardUserAggregateCoverageKind = iota
	dashboardUserAggregateCoverageDaily
)

func dashboardUserUsageTrendCoverageKind(startTime, endTime time.Time, granularity string) (dashboardUserAggregateCoverageKind, bool) {
	switch granularity {
	case "hour":
		return dashboardUserAggregateCoverageHourly, true
	case "day":
		if isWholeDashboardDayRange(startTime, endTime) {
			return dashboardUserAggregateCoverageDaily, true
		}
		// A day-level report in a timezone different from the server is not aligned
		// to the server's daily buckets. The hourly aggregate is still safe and can
		// be grouped by the requested calendar day without falling back to raw logs.
		return dashboardUserAggregateCoverageHourly, true
	default:
		return dashboardUserAggregateCoverageHourly, false
	}
}

func (r *usageLogRepository) dashboardUserAggregateCoveredEnd(ctx context.Context, startTime, endTime time.Time, kind dashboardUserAggregateCoverageKind) (time.Time, bool, error) {
	if !endTime.After(time.Unix(0, 0).UTC()) {
		return time.Time{}, false, nil
	}
	fromColumn := "user_hourly_aggregated_from"
	toColumn := "user_hourly_last_aggregated_at"
	if kind == dashboardUserAggregateCoverageDaily {
		fromColumn = "user_daily_aggregated_from"
		toColumn = "user_daily_last_aggregated_at"
	}
	var coveredFrom, coveredTo time.Time
	query := fmt.Sprintf("SELECT %s, %s FROM usage_dashboard_aggregation_watermark WHERE id = 1", fromColumn, toColumn)
	if err := scanSingleRow(ctx, r.sql, query, nil, &coveredFrom, &coveredTo); err != nil {
		if err == sql.ErrNoRows || isUndefinedTableError(err) || isUndefinedColumnError(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	allowFutureTail := kind == dashboardUserAggregateCoverageHourly
	aggregateEnd, covered := dashboardAggregateCoveredEnd(startTime, endTime, coveredFrom, coveredTo, allowFutureTail)
	return aggregateEnd, covered, nil
}

func scanUserSpendingRankingRows(rows *sql.Rows) ([]UserSpendingRankingItem, float64, int64, int64, error) {
	ranking := make([]UserSpendingRankingItem, 0)
	totalActualCost := 0.0
	totalRequests := int64(0)
	totalTokens := int64(0)
	for rows.Next() {
		var row UserSpendingRankingItem
		if err := rows.Scan(&row.UserID, &row.Email, &row.Username, &row.ActualCost, &row.Requests, &row.Tokens, &totalActualCost, &totalRequests, &totalTokens); err != nil {
			return nil, 0, 0, 0, err
		}
		ranking = append(ranking, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, 0, err
	}
	return ranking, totalActualCost, totalRequests, totalTokens, nil
}

// UserDashboardStats 用户仪表盘统计
type UserDashboardStats = usagestats.UserDashboardStats

// PlatformDashboardStats 单平台用量明细
type PlatformDashboardStats = usagestats.PlatformDashboardStats

// GetUserDashboardStats 获取用户专属的仪表盘统计
func (r *usageLogRepository) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	today := timezone.Today()

	// API Key 统计
	if err := scanSingleRow(
		ctx,
		r.sql,
		"SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND deleted_at IS NULL",
		[]any{userID},
		&stats.TotalAPIKeys,
	); err != nil {
		return nil, err
	}
	if err := scanSingleRow(
		ctx,
		r.sql,
		"SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND status = $2 AND deleted_at IS NULL",
		[]any{userID, service.StatusActive},
		&stats.ActiveAPIKeys,
	); err != nil {
		return nil, err
	}

	// 累计 Token 统计
	totalStatsQuery := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM usage_logs
		WHERE user_id = $1
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		totalStatsQuery,
		[]any{userID},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens

	// 今日 Token 统计
	todayStatsQuery := `
		SELECT
			COUNT(*) as today_requests,
			COALESCE(SUM(input_tokens), 0) as today_input_tokens,
			COALESCE(SUM(output_tokens), 0) as today_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as today_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as today_cost,
			COALESCE(SUM(actual_cost), 0) as today_actual_cost
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		todayStatsQuery,
		[]any{userID, today},
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
	); err != nil {
		return nil, err
	}
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	// 性能指标：RPM 和 TPM（最近1分钟，仅统计该用户的请求）
	rpm, tpm, err := r.getPerformanceStats(ctx, userID)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	// 按"有效平台"维度拆分（group.platform 优先，否则 account.platform）。
	// 与 ops 路径口径一致；HAVING 过滤掉无法确定平台的行（避免出现空字符串平台）。
	// 与上面 totalStatsQuery/todayStatsQuery 的总值可能略微差异，原因有二：
	//   1) 无平台归属的极少数行（group/account 都没 platform）会被 HAVING 排除；
	//   2) usageLogSuccessFilterUL 会把 actual_cost = 0 的失败 placeholder 行排除，
	//      而 totalStatsQuery/todayStatsQuery 没有这层过滤、会把这些行的 request 计数算进去。
	platformQuery := `
		SELECT
			` + usageLogEffectivePlatformExpr + ` as platform,
			COUNT(*) as total_requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.actual_cost), 0) as total_actual_cost,
			COUNT(*) FILTER (WHERE ul.created_at >= $2) as today_requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens) FILTER (WHERE ul.created_at >= $2), 0) as today_tokens,
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $2), 0) as today_actual_cost
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		LEFT JOIN accounts a ON a.id = ul.account_id
		WHERE ul.user_id = $1
		  AND ` + usageLogSuccessFilterUL + `
		GROUP BY ` + usageLogEffectivePlatformExpr + `
		HAVING ` + usageLogEffectivePlatformExpr + ` IS NOT NULL AND ` + usageLogEffectivePlatformExpr + ` <> ''
		ORDER BY total_actual_cost DESC
	`
	rows, err := r.sql.QueryContext(ctx, platformQuery, userID, today)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p PlatformDashboardStats
		if err := rows.Scan(
			&p.Platform,
			&p.TotalRequests,
			&p.TotalTokens,
			&p.TotalActualCost,
			&p.TodayRequests,
			&p.TodayTokens,
			&p.TodayActualCost,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stats.ByPlatform = append(stats.ByPlatform, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// getPerformanceStatsByAPIKey 获取指定 API Key 的 RPM 和 TPM（近5分钟平均值）
func (r *usageLogRepository) getPerformanceStatsByAPIKey(ctx context.Context, apiKeyID int64) (rpm, tpm int64, err error) {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	query := `
		SELECT
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as token_count
		FROM usage_logs
		WHERE created_at >= $1 AND api_key_id = $2`
	args := []any{fiveMinutesAgo, apiKeyID}

	var requestCount int64
	var tokenCount int64
	if err := scanSingleRow(ctx, r.sql, query, args, &requestCount, &tokenCount); err != nil {
		return 0, 0, err
	}
	return requestCount / 5, tokenCount / 5, nil
}

// GetAPIKeyDashboardStats 获取指定 API Key 的仪表盘统计（按 api_key_id 过滤）
func (r *usageLogRepository) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	stats := &UserDashboardStats{}
	today := timezone.Today()

	// API Key 维度不需要统计 key 数量，设为 1
	stats.TotalAPIKeys = 1
	stats.ActiveAPIKeys = 1

	// 累计 Token 统计
	totalStatsQuery := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM usage_logs
		WHERE api_key_id = $1
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		totalStatsQuery,
		[]any{apiKeyID},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheCreationTokens + stats.TotalCacheReadTokens

	// 今日 Token 统计
	todayStatsQuery := `
		SELECT
			COUNT(*) as today_requests,
			COALESCE(SUM(input_tokens), 0) as today_input_tokens,
			COALESCE(SUM(output_tokens), 0) as today_output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as today_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as today_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as today_cost,
			COALESCE(SUM(actual_cost), 0) as today_actual_cost
		FROM usage_logs
		WHERE api_key_id = $1 AND created_at >= $2
	`
	if err := scanSingleRow(
		ctx,
		r.sql,
		todayStatsQuery,
		[]any{apiKeyID, today},
		&stats.TodayRequests,
		&stats.TodayInputTokens,
		&stats.TodayOutputTokens,
		&stats.TodayCacheCreationTokens,
		&stats.TodayCacheReadTokens,
		&stats.TodayCost,
		&stats.TodayActualCost,
	); err != nil {
		return nil, err
	}
	stats.TodayTokens = stats.TodayInputTokens + stats.TodayOutputTokens + stats.TodayCacheCreationTokens + stats.TodayCacheReadTokens

	// 性能指标：RPM 和 TPM（最近5分钟，按 API Key 过滤）
	rpm, tpm, err := r.getPerformanceStatsByAPIKey(ctx, apiKeyID)
	if err != nil {
		return nil, err
	}
	stats.Rpm = rpm
	stats.Tpm = tpm

	return stats, nil
}

func appendUsageTrendFilters(query string, args []any, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) (string, []any) {
	if userID > 0 {
		query += fmt.Sprintf(" AND user_id = $%d", len(args)+1)
		args = append(args, userID)
	}
	if apiKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, apiKeyID)
	}
	if accountID > 0 {
		query += fmt.Sprintf(" AND account_id = $%d", len(args)+1)
		args = append(args, accountID)
	}
	if groupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, groupID)
	}
	query, args = appendRawUsageLogModelQueryFilter(query, args, model)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, requestType, stream)
	if billingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*billingType))
	}
	return query, args
}

func (r *usageLogRepository) getModelStatsFromDashboardAggregates(ctx context.Context, startTime, endTime time.Time) (results []ModelStat, err error) {
	aggregateEnd, covered, coverageErr := r.dashboardModelAggregateCoveredEnd(ctx, startTime, endTime)
	if coverageErr != nil {
		return nil, coverageErr
	}
	if !covered {
		return []ModelStat{}, nil
	}
	tableName := "usage_dashboard_model_hourly"
	where := "bucket_start >= $1 AND bucket_start < $2"
	if isWholeDashboardDayRange(startTime, aggregateEnd) {
		tableName = "usage_dashboard_model_daily"
		where = "bucket_date >= $1::date AND bucket_date < $2::date"
	}
	query := fmt.Sprintf(`
		WITH aggregated AS (
		SELECT
			model,
			COALESCE(SUM(total_requests), 0) AS requests,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost), 0) AS cost,
			COALESCE(SUM(actual_cost), 0) AS actual_cost,
			COALESCE(SUM(account_cost), 0) AS account_cost
		FROM %s
		WHERE %s
		GROUP BY model
		)
		SELECT
			aggregated.model,
			aggregated.requests,
			aggregated.input_tokens,
			aggregated.output_tokens,
			aggregated.cache_creation_tokens,
			aggregated.cache_read_tokens,
			aggregated.total_tokens,
			aggregated.cost,
			aggregated.actual_cost,
			aggregated.account_cost
		FROM aggregated
		ORDER BY total_tokens DESC
	`, tableName, where)
	rows, err := r.sql.QueryContext(ctx, query, startTime, aggregateEnd)
	if err != nil {
		if isUndefinedTableError(err) {
			return []ModelStat{}, nil
		}
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()
	return scanModelStatsRows(rows)
}

func (r *usageLogRepository) dashboardModelAggregateCoveredEnd(ctx context.Context, startTime, endTime time.Time) (time.Time, bool, error) {
	var coveredFrom, coveredTo time.Time
	query := `
		SELECT model_hourly_aggregated_from, model_hourly_last_aggregated_at
		FROM usage_dashboard_aggregation_watermark
		WHERE id = 1
	`
	if err := scanSingleRow(ctx, r.sql, query, nil, &coveredFrom, &coveredTo); err != nil {
		if err == sql.ErrNoRows || isUndefinedTableError(err) || isUndefinedColumnError(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	aggregateEnd, covered := dashboardAggregateCoveredEnd(startTime, endTime, coveredFrom, coveredTo, true)
	return aggregateEnd, covered, nil
}

// GetDashboardModelAggregateCoverage exposes model aggregate readiness without
// changing the model-stat repository method used by other services.
func (r *usageLogRepository) GetDashboardModelAggregateCoverage(ctx context.Context, startTime, endTime time.Time) (usagestats.DashboardAggregateCoverage, error) {
	aggregateEnd, covered, err := r.dashboardModelAggregateCoveredEnd(ctx, startTime, endTime)
	if err != nil {
		return usagestats.DashboardAggregateCoverage{}, err
	}
	return dashboardAggregateCoverage(startTime, endTime, aggregateEnd, covered), nil
}

func dashboardAggregateCoverage(startTime, endTime, aggregateEnd time.Time, available bool) usagestats.DashboardAggregateCoverage {
	coverage := usagestats.DashboardAggregateCoverage{DataAvailable: available}
	if !available {
		return coverage
	}
	coverage.AggregationComplete = !aggregateEnd.Before(endTime)
	coverage.CoverageStart = startTime.UTC().Format(time.RFC3339)
	coverage.CoverageEnd = aggregateEnd.UTC().Format(time.RFC3339)
	return coverage
}

// dashboardAggregateCoveredEnd validates the immutable start boundary and
// returns the safe aggregate query end. Historical gaps remain unavailable. A
// request whose end is still in the future (the dashboard's inclusive current
// date shape) may use the covered realtime prefix without querying raw logs.
func dashboardAggregateCoveredEnd(startTime, endTime, coveredFrom, coveredTo time.Time, allowFutureTail bool) (time.Time, bool) {
	epoch := time.Unix(0, 0).UTC()
	startUTC := startTime.UTC()
	endUTC := endTime.UTC()
	coveredFromUTC := coveredFrom.UTC()
	coveredToUTC := coveredTo.UTC()
	if !endUTC.After(startUTC) || !coveredToUTC.After(epoch) || startUTC.Before(coveredFromUTC) {
		return time.Time{}, false
	}
	if !endUTC.After(coveredToUTC) {
		return endTime, true
	}
	if !allowFutureTail || !endUTC.After(time.Now().UTC()) || !coveredToUTC.After(startUTC) {
		return time.Time{}, false
	}
	return coveredTo, true
}

func buildWhere(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(conditions, " AND ")
}

func appendRequestTypeOrStreamWhereCondition(conditions []string, args []any, requestType *int16, stream *bool) ([]string, []any) {
	if requestType != nil {
		condition, conditionArgs := buildRequestTypeFilterCondition(len(args)+1, *requestType)
		conditions = append(conditions, condition)
		args = append(args, conditionArgs...)
		return conditions, args
	}
	if stream != nil {
		conditions = append(conditions, fmt.Sprintf("stream = $%d", len(args)+1))
		args = append(args, *stream)
	}
	return conditions, args
}

func appendRequestTypeOrStreamQueryFilter(query string, args []any, requestType *int16, stream *bool) (string, []any) {
	return appendRequestTypeOrStreamQueryFilterWithAlias(query, args, requestType, stream, "")
}

func appendRequestTypeOrStreamQueryFilterWithAlias(query string, args []any, requestType *int16, stream *bool, alias string) (string, []any) {
	if requestType != nil {
		condition, conditionArgs := buildRequestTypeFilterConditionWithAlias(len(args)+1, *requestType, alias)
		query += " AND " + condition
		args = append(args, conditionArgs...)
		return query, args
	}
	if stream != nil {
		query += fmt.Sprintf(" AND %s = $%d", usageLogQualifiedColumn(alias, "stream"), len(args)+1)
		args = append(args, *stream)
	}
	return query, args
}

// buildRequestTypeFilterCondition 在 request_type 过滤时兼容 legacy 字段，避免历史数据漏查。
func buildRequestTypeFilterCondition(startArgIndex int, requestType int16) (string, []any) {
	return buildRequestTypeFilterConditionWithAlias(startArgIndex, requestType, "")
}

func buildRequestTypeFilterConditionWithAlias(startArgIndex int, requestType int16, alias string) (string, []any) {
	normalized := service.RequestTypeFromInt16(requestType)
	requestTypeArg := int16(normalized)
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	switch normalized {
	case service.RequestTypeSync:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sstream = FALSE AND %sopenai_ws_mode = FALSE))", prefix, startArgIndex, prefix, int16(service.RequestTypeUnknown), prefix, prefix), []any{requestTypeArg}
	case service.RequestTypeStream:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sstream = TRUE AND %sopenai_ws_mode = FALSE))", prefix, startArgIndex, prefix, int16(service.RequestTypeUnknown), prefix, prefix), []any{requestTypeArg}
	case service.RequestTypeWSV2:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sopenai_ws_mode = TRUE))", prefix, startArgIndex, prefix, int16(service.RequestTypeUnknown), prefix), []any{requestTypeArg}
	default:
		return fmt.Sprintf("%srequest_type = $%d", prefix, startArgIndex), []any{requestTypeArg}
	}
}
