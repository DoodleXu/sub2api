package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// TrendDataPoint represents a single point in trend data
type TrendDataPoint = usagestats.TrendDataPoint

// ModelStat represents usage statistics for a single model
type ModelStat = usagestats.ModelStat

// UserUsageTrendPoint represents user usage trend data point
type UserUsageTrendPoint = usagestats.UserUsageTrendPoint

// UserSpendingRankingItem represents a user spending ranking row.
type UserSpendingRankingItem = usagestats.UserSpendingRankingItem
type UserSpendingRankingResponse = usagestats.UserSpendingRankingResponse

// APIKeyUsageTrendPoint represents API key usage trend data point
type APIKeyUsageTrendPoint = usagestats.APIKeyUsageTrendPoint

// GetAPIKeyUsageTrend returns usage trend data grouped by API key and date
func (r *usageLogRepository) GetAPIKeyUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) (results []APIKeyUsageTrendPoint, err error) {
	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		WITH top_keys AS (
			SELECT api_key_id
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY api_key_id
			ORDER BY SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) DESC
			LIMIT $3
		)
		SELECT
			TO_CHAR(u.created_at, '%s') as date,
			u.api_key_id,
			COALESCE(k.name, '') as key_name,
			COUNT(*) as requests,
			COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) as tokens
		FROM usage_logs u
		LEFT JOIN api_keys k ON u.api_key_id = k.id
		WHERE u.api_key_id IN (SELECT api_key_id FROM top_keys)
		  AND u.created_at >= $4 AND u.created_at < $5
		GROUP BY date, u.api_key_id, k.name
		ORDER BY date ASC, tokens DESC
	`, dateFormat)

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 保持主错误优先；仅在无错误时回传 Close 失败。
		// 同时清空返回值，避免误用不完整结果。
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]APIKeyUsageTrendPoint, 0)
	for rows.Next() {
		var row APIKeyUsageTrendPoint
		if err = rows.Scan(&row.Date, &row.APIKeyID, &row.KeyName, &row.Requests, &row.Tokens); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// GetUserUsageTrend returns usage trend data grouped by user and date
func (r *usageLogRepository) GetUserUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int, metric string) (results []UserUsageTrendPoint, err error) {
	coverageKind, canCheckCoverage := dashboardUserUsageTrendCoverageKind(startTime, endTime, granularity)
	if canCheckCoverage {
		if aggregateEnd, canUseAggregates, coverageErr := r.dashboardUserAggregateCoveredEnd(ctx, startTime, endTime, coverageKind); coverageErr != nil {
			return nil, coverageErr
		} else if canUseAggregates {
			if aggregated, _, aggregatedErr := r.getUserUsageTrendFromAggregates(ctx, startTime, aggregateEnd, granularity, limit, coverageKind, metric); aggregatedErr != nil {
				if !isUndefinedTableError(aggregatedErr) {
					return nil, aggregatedErr
				}
			} else {
				return aggregated, nil
			}
		}
		// Daily tables intentionally cover complete days only. When a day-level
		// request includes the current unfinished day, group the covered hourly
		// buckets by day instead of returning an empty section or reading raw logs.
		if coverageKind == dashboardUserAggregateCoverageDaily {
			if aggregateEnd, canUseHourly, coverageErr := r.dashboardUserAggregateCoveredEnd(ctx, startTime, endTime, dashboardUserAggregateCoverageHourly); coverageErr != nil {
				return nil, coverageErr
			} else if canUseHourly {
				if aggregated, _, aggregatedErr := r.getUserUsageTrendFromAggregates(ctx, startTime, aggregateEnd, granularity, limit, dashboardUserAggregateCoverageHourly, metric); aggregatedErr != nil {
					if !isUndefinedTableError(aggregatedErr) {
						return nil, aggregatedErr
					}
				} else {
					return aggregated, nil
				}
			}
		}
	}
	// Dashboard widgets must never fall back to grouping raw usage_logs. Missing
	// aggregate coverage is represented as an empty optional section instead.
	return []UserUsageTrendPoint{}, nil
}

// GetUserSpendingRanking returns user spending ranking aggregated within the time range.
func (r *usageLogRepository) GetUserSpendingRanking(ctx context.Context, startTime, endTime time.Time, limit int) (result *UserSpendingRankingResponse, err error) {
	if limit <= 0 {
		limit = 12
	}
	if isWholeDashboardDayRange(startTime, endTime) {
		if aggregateEnd, canUseDaily, coverageErr := r.dashboardUserAggregateCoveredEnd(ctx, startTime, endTime, dashboardUserAggregateCoverageDaily); coverageErr != nil {
			return nil, coverageErr
		} else if canUseDaily {
			if aggregated, _, aggregatedErr := r.getUserSpendingRankingFromAggregates(ctx, startTime, aggregateEnd, limit); aggregatedErr != nil {
				if !isUndefinedTableError(aggregatedErr) {
					return nil, aggregatedErr
				}
			} else {
				aggregated.DashboardAggregateCoverage = dashboardAggregateCoverage(startTime, endTime, aggregateEnd, true)
				return aggregated, nil
			}
		}
	}
	if aggregateEnd, canUseHourly, coverageErr := r.dashboardUserAggregateCoveredEnd(ctx, startTime, endTime, dashboardUserAggregateCoverageHourly); coverageErr != nil {
		return nil, coverageErr
	} else if canUseHourly {
		if aggregated, _, aggregatedErr := r.getUserSpendingRankingFromHourlyAggregates(ctx, startTime, aggregateEnd, limit); aggregatedErr != nil {
			if !isUndefinedTableError(aggregatedErr) {
				return nil, aggregatedErr
			}
		} else {
			aggregated.DashboardAggregateCoverage = dashboardAggregateCoverage(startTime, endTime, aggregateEnd, true)
			return aggregated, nil
		}
	}
	return &UserSpendingRankingResponse{
		Ranking:                    []UserSpendingRankingItem{},
		DashboardAggregateCoverage: dashboardAggregateCoverage(startTime, endTime, time.Time{}, false),
	}, nil
}

// GetUserUsageTrendByUserID 获取指定用户的使用趋势
func (r *usageLogRepository) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) (results []TrendDataPoint, err error) {
	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(created_at, '%s') as date,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY date
		ORDER BY date ASC
	`, dateFormat)

	rows, err := r.sql.QueryContext(ctx, query, userID, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 保持主错误优先；仅在无错误时回传 Close 失败。
		// 同时清空返回值，避免误用不完整结果。
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetUserModelStats 获取指定用户的模型统计
func (r *usageLogRepository) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, userID, 0, 0, 0, nil, nil, nil, usagestats.ModelSourceRequested)
}

// GetUsageTrendWithFilters returns usage trend data with optional filters
func (r *usageLogRepository) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) (results []TrendDataPoint, err error) {
	if shouldUsePreaggregatedTrend(granularity, userID, apiKeyID, accountID, groupID, model, requestType, stream, billingType) {
		aggregated, aggregatedErr := r.getUsageTrendFromAggregates(ctx, startTime, endTime, granularity)
		if aggregatedErr == nil && len(aggregated) > 0 {
			return aggregated, nil
		}
	}

	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(created_at, '%s') as date,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, dateFormat)

	args := []any{startTime, endTime}
	query, args = appendUsageTrendFilters(query, args, userID, apiKeyID, accountID, groupID, model, requestType, stream, billingType)
	query += " GROUP BY date ORDER BY date ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 保持主错误优先；仅在无错误时回传 Close 失败。
		// 同时清空返回值，避免误用不完整结果。
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (r *usageLogRepository) GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters usagestats.UsageLogFilters) (results []TrendDataPoint, err error) {
	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(created_at, '%s') as date,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, dateFormat)

	args := []any{startTime, endTime}
	if filters.UserID > 0 {
		query += fmt.Sprintf(" AND user_id = $%d", len(args)+1)
		args = append(args, filters.UserID)
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.AccountID > 0 {
		query += fmt.Sprintf(" AND account_id = $%d", len(args)+1)
		args = append(args, filters.AccountID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, filters.ModelFilterSource, "")
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "")
	query = appendUsageLogUpstreamModelMismatchQueryFilter(query, filters.UpstreamModelMismatch, "")
	query += " GROUP BY date ORDER BY date ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func shouldUsePreaggregatedTrend(granularity string, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) bool {
	if granularity != "day" && granularity != "hour" {
		return false
	}
	return userID == 0 &&
		apiKeyID == 0 &&
		accountID == 0 &&
		groupID == 0 &&
		model == "" &&
		requestType == nil &&
		stream == nil &&
		billingType == nil
}

func (r *usageLogRepository) getUsageTrendFromAggregates(ctx context.Context, startTime, endTime time.Time, granularity string) (results []TrendDataPoint, err error) {
	dateFormat := safeDateFormat(granularity)
	query := ""
	args := []any{startTime, endTime}

	switch granularity {
	case "hour":
		query = fmt.Sprintf(`
			SELECT
				TO_CHAR(bucket_start, '%s') as date,
				total_requests as requests,
				input_tokens,
					output_tokens,
					cache_creation_tokens,
					cache_read_tokens,
					(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) as total_tokens,
					total_cost as cost,
					actual_cost
				FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			ORDER BY bucket_start ASC
		`, dateFormat)
	case "day":
		query = fmt.Sprintf(`
			SELECT
				TO_CHAR(bucket_date::timestamp, '%s') as date,
				total_requests as requests,
				input_tokens,
					output_tokens,
					cache_creation_tokens,
					cache_read_tokens,
					(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) as total_tokens,
					total_cost as cost,
					actual_cost
				FROM usage_dashboard_daily
			WHERE bucket_date >= $1::date AND bucket_date < $2::date
			ORDER BY bucket_date ASC
		`, dateFormat)
	default:
		return nil, nil
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetModelStatsWithFilters returns model statistics with optional filters
func (r *usageLogRepository) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, requestType, stream, billingType, usagestats.ModelSourceRequested)
}

// GetModelStatsWithFiltersBySource returns model statistics with optional filters and model source dimension.
// source: requested | upstream | mapping.
func (r *usageLogRepository) GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, requestType, stream, billingType, source)
}

func (r *usageLogRepository) GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters usagestats.UsageLogFilters, source string) (results []ModelStat, err error) {
	modelExpr := usageLogModelDimensionExpression(source, "ul")

	query := fmt.Sprintf(`
		SELECT
			%s as model,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens), 0) as input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) as output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			%s as account_cost
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, modelExpr, usageAccountCostSumExpr)

	args := []any{startTime, endTime}
	if filters.UserID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, filters.UserID)
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.AccountID > 0 {
		query += fmt.Sprintf(" AND ul.account_id = $%d", len(args)+1)
		args = append(args, filters.AccountID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, source, "ul")
	query, args = appendRequestTypeOrStreamQueryFilterWithAlias(query, args, filters.RequestType, filters.Stream, "ul")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "ul")
	query = appendUsageLogUpstreamModelMismatchQueryFilter(query, filters.UpstreamModelMismatch, "ul")
	query += fmt.Sprintf(" GROUP BY %s ORDER BY total_tokens DESC", modelExpr)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanModelStatsRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (r *usageLogRepository) getModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) (results []ModelStat, err error) {
	if userID == 0 && apiKeyID == 0 && accountID == 0 && groupID == 0 && requestType == nil && stream == nil && billingType == nil && usagestats.NormalizeModelSource(source) == usagestats.ModelSourceRequested {
		return r.getModelStatsFromDashboardAggregates(ctx, startTime, endTime)
	}
	modelExpr := usageLogModelDimensionExpression(source, "ul")

	query := fmt.Sprintf(`
		SELECT
			%s as model,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens), 0) as input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) as output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			%s as account_cost
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, modelExpr, usageAccountCostSumExpr)

	args := []any{startTime, endTime}
	if userID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, userID)
	}
	if apiKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, apiKeyID)
	}
	if accountID > 0 {
		query += fmt.Sprintf(" AND ul.account_id = $%d", len(args)+1)
		args = append(args, accountID)
	}
	if groupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, groupID)
	}
	query, args = appendRequestTypeOrStreamQueryFilterWithAlias(query, args, requestType, stream, "ul")
	if billingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*billingType))
	}
	query += fmt.Sprintf(" GROUP BY %s ORDER BY total_tokens DESC", modelExpr)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// 保持主错误优先；仅在无错误时回传 Close 失败。
		// 同时清空返回值，避免误用不完整结果。
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanModelStatsRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetGroupStatsWithFilters returns group usage statistics with optional filters
func (r *usageLogRepository) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []usagestats.GroupStat, err error) {
	query := fmt.Sprintf(`
		SELECT
			COALESCE(ul.group_id, 0) as group_id,
			COALESCE(g.name, '') as group_name,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			%s as account_cost
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, usageAccountCostSumExpr)

	args := []any{startTime, endTime}
	if userID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, userID)
	}
	if apiKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, apiKeyID)
	}
	if accountID > 0 {
		query += fmt.Sprintf(" AND ul.account_id = $%d", len(args)+1)
		args = append(args, accountID)
	}
	if groupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, groupID)
	}
	query, args = appendRequestTypeOrStreamQueryFilterWithAlias(query, args, requestType, stream, "ul")
	if billingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*billingType))
	}
	query += " GROUP BY ul.group_id, g.name ORDER BY total_tokens DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.GroupStat, 0)
	for rows.Next() {
		var row usagestats.GroupStat
		if err := rows.Scan(
			&row.GroupID,
			&row.GroupName,
			&row.Requests,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.AccountCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *usageLogRepository) GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters usagestats.UsageLogFilters) (results []usagestats.GroupStat, err error) {
	query := fmt.Sprintf(`
		SELECT
			COALESCE(ul.group_id, 0) as group_id,
			COALESCE(g.name, '') as group_name,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			%s as account_cost
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, usageAccountCostSumExpr)

	args := []any{startTime, endTime}
	if filters.UserID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, filters.UserID)
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.AccountID > 0 {
		query += fmt.Sprintf(" AND ul.account_id = $%d", len(args)+1)
		args = append(args, filters.AccountID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, filters.ModelFilterSource, "ul")
	query, args = appendRequestTypeOrStreamQueryFilterWithAlias(query, args, filters.RequestType, filters.Stream, "ul")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "ul")
	query = appendUsageLogUpstreamModelMismatchQueryFilter(query, filters.UpstreamModelMismatch, "ul")
	query += " GROUP BY ul.group_id, g.name ORDER BY total_tokens DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.GroupStat, 0)
	for rows.Next() {
		var row usagestats.GroupStat
		if err := rows.Scan(
			&row.GroupID,
			&row.GroupName,
			&row.Requests,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.AccountCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetUserBreakdownStats returns per-user usage breakdown within a specific dimension.
func (r *usageLogRepository) GetUserBreakdownStats(ctx context.Context, startTime, endTime time.Time, dim usagestats.UserBreakdownDimension, limit int) (results []usagestats.UserBreakdownItem, err error) {
	query := fmt.Sprintf(`
		SELECT
			COALESCE(ul.user_id, 0) as user_id,
			COALESCE(u.email, '') as email,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			%s as account_cost
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = ul.user_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, usageAccountCostSumExpr)
	args := []any{startTime, endTime}

	if dim.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, dim.GroupID)
	}
	if dim.Model != "" {
		query += fmt.Sprintf(" AND %s = $%d", resolveModelDimensionExpression(dim.ModelType), len(args)+1)
		args = append(args, dim.Model)
	}
	if dim.Endpoint != "" {
		col := resolveEndpointColumn(dim.EndpointType)
		query += fmt.Sprintf(" AND %s = $%d", col, len(args)+1)
		args = append(args, dim.Endpoint)
	}
	if dim.UserID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, dim.UserID)
	}
	if dim.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, dim.APIKeyID)
	}
	if dim.AccountID > 0 {
		query += fmt.Sprintf(" AND ul.account_id = $%d", len(args)+1)
		args = append(args, dim.AccountID)
	}
	if dim.RequestType != nil {
		condition, conditionArgs := buildRequestTypeFilterConditionWithAlias(len(args)+1, *dim.RequestType, "ul")
		query += " AND " + condition
		args = append(args, conditionArgs...)
	}
	if dim.Stream != nil {
		query += fmt.Sprintf(" AND ul.stream = $%d", len(args)+1)
		args = append(args, *dim.Stream)
	}
	if dim.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, *dim.BillingType)
	}
	if dim.NativeCompactionV2 != nil {
		query += fmt.Sprintf(" AND ul.native_compaction_v2 = $%d", len(args)+1)
		args = append(args, *dim.NativeCompactionV2)
	}

	query += " GROUP BY ul.user_id, u.email ORDER BY actual_cost DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.UserBreakdownItem, 0)
	for rows.Next() {
		var row usagestats.UserBreakdownItem
		if err := rows.Scan(
			&row.UserID,
			&row.Email,
			&row.Requests,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.AccountCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAllGroupUsageSummary returns today's, yesterday's, and cumulative actual_cost
// for every group using the persisted daily rollups plus the unpublished raw tail.
func (r *usageLogRepository) GetAllGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]usagestats.GroupUsageSummary, error) {
	return r.getAllGroupUsageSummaryFromRollups(ctx, todayStart)
}

// resolveModelDimensionExpression maps model source type to a safe SQL expression.
func resolveModelDimensionExpression(modelType string) string {
	requestedExpr := "COALESCE(NULLIF(TRIM(requested_model), ''), model)"
	upstreamExpr := "COALESCE(NULLIF(TRIM(upstream_model), ''), model)"
	switch usagestats.NormalizeModelSource(modelType) {
	case usagestats.ModelSourceUpstream:
		return upstreamExpr
	case usagestats.ModelSourceMapping:
		return fmt.Sprintf("(%s || ' -> ' || %s)", requestedExpr, upstreamExpr)
	default:
		return requestedExpr
	}
}

func scanTrendRows(rows *sql.Rows) ([]TrendDataPoint, error) {
	results := make([]TrendDataPoint, 0)
	for rows.Next() {
		var row TrendDataPoint
		if err := rows.Scan(
			&row.Date,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func scanModelStatsRows(rows *sql.Rows) ([]ModelStat, error) {
	results := make([]ModelStat, 0)
	for rows.Next() {
		var row ModelStat
		if err := rows.Scan(
			&row.Model,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.AccountCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
