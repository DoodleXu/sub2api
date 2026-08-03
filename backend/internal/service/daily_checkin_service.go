package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrDailyCheckinAlreadyCheckedIn = infraerrors.Conflict("DAILY_CHECKIN_ALREADY_CHECKED_IN", "already checked in today")
	ErrDailyCheckinUsageNotEnough   = infraerrors.BadRequest("DAILY_CHECKIN_USAGE_NOT_ENOUGH", "daily usage is less than required")
	ErrDailyCheckinDisabled         = infraerrors.Forbidden("DAILY_CHECKIN_DISABLED", "daily check-in is disabled")
	ErrDailyCheckinBudgetExhausted  = infraerrors.Forbidden("DAILY_CHECKIN_BUDGET_EXHAUSTED", "daily check-in reward budget has been exhausted")
)

type DailyCheckinRecord struct {
	ID                int64                       `json:"-"`
	UserID            int64                       `json:"-"`
	Date              string                      `json:"date"`
	RewardAmount      float64                     `json:"reward_amount"`
	QualifiedUsageUSD float64                     `json:"qualified_usage_usd"`
	RewardMetadata    *DailyCheckinRewardMetadata `json:"reward_metadata,omitempty"`
	RedeemCodeID      *int64                      `json:"-"`
	CreatedAt         time.Time                   `json:"created_at"`
}

type DailyCheckinStatus struct {
	Enabled              bool                 `json:"enabled"`
	Today                string               `json:"today"`
	Month                string               `json:"month"`
	CheckedIn            bool                 `json:"checked_in"`
	Eligible             bool                 `json:"eligible"`
	TodayUsageUSD        float64              `json:"today_usage_usd"`
	RequiredUsageUSD     float64              `json:"required_usage_usd"`
	UsageScope           string               `json:"-"`
	RewardMinUSD         float64              `json:"reward_min_usd"`
	RewardMaxUSD         float64              `json:"reward_max_usd"`
	DailyBudgetUSD       float64              `json:"-"`
	DailyRewardUSD       float64              `json:"-"`
	MonthlyBudgetUSD     float64              `json:"-"`
	MonthlyRewardUSD     float64              `json:"-"`
	UserMonthlyLimitUSD  float64              `json:"-"`
	UserMonthlyRewardUSD float64              `json:"-"`
	BudgetExhausted      bool                 `json:"-"`
	MonthCheckins        []DailyCheckinRecord `json:"month_checkins"`
}

type DailyCheckinResult struct {
	DailyCheckinStatus
	RewardAmount        float64   `json:"reward_amount"`
	BaseRewardAmount    float64   `json:"base_reward_amount"`
	Message             string    `json:"message,omitempty"`
	BudgetFallback      bool      `json:"budget_fallback"`
	StreakDays          int       `json:"streak_days"`
	StreakMultiplier    float64   `json:"streak_multiplier"`
	CritHit             bool      `json:"crit_hit"`
	CritMultiplier      float64   `json:"crit_multiplier"`
	PreCritRewardAmount float64   `json:"pre_crit_reward_amount"`
	Balance             float64   `json:"balance"`
	CheckedInAt         time.Time `json:"checked_in_at"`
}

type DailyCheckinRewardMetadata struct {
	BaseRewardAmount    float64                 `json:"base_reward_amount"`
	RewardTier          *DailyCheckinRewardTier `json:"reward_tier,omitempty"`
	BudgetFallback      bool                    `json:"budget_fallback"`
	BudgetFallbackText  string                  `json:"budget_fallback_message,omitempty"`
	StreakDays          int                     `json:"streak_days"`
	StreakMultiplier    float64                 `json:"streak_multiplier"`
	CritEligible        bool                    `json:"crit_eligible"`
	CritHit             bool                    `json:"crit_hit"`
	CritMultiplier      float64                 `json:"crit_multiplier"`
	PreCritRewardAmount float64                 `json:"pre_crit_reward_amount"`
	FinalRewardAmount   float64                 `json:"final_reward_amount"`
	RequiredUsageUSD    float64                 `json:"required_usage_usd,omitempty"`
	UsageScope          string                  `json:"usage_scope,omitempty"`
	RuleEffectiveAt     string                  `json:"rule_effective_at,omitempty"`
}

type DailyCheckinAdminStats struct {
	Enabled             bool                `json:"enabled"`
	RequiredUsageUSD    float64             `json:"required_usage_usd"`
	UsageScope          string              `json:"usage_scope"`
	RewardMinUSD        float64             `json:"reward_min_usd"`
	RewardMaxUSD        float64             `json:"reward_max_usd"`
	TodayCheckins       int64               `json:"today_checkins"`
	TodayUsers          int64               `json:"today_users"`
	TodayRewardUSD      float64             `json:"today_reward_usd"`
	MonthCheckins       int64               `json:"month_checkins"`
	MonthUsers          int64               `json:"month_users"`
	MonthRewardUSD      float64             `json:"month_reward_usd"`
	AverageRewardUSD    float64             `json:"average_reward_usd"`
	DailyBudgetUSD      float64             `json:"daily_budget_usd"`
	DailyRemainingUSD   float64             `json:"daily_remaining_usd"`
	MonthlyBudgetUSD    float64             `json:"monthly_budget_usd"`
	MonthlyRemainingUSD float64             `json:"monthly_remaining_usd"`
	UserMonthlyLimitUSD float64             `json:"user_monthly_limit_usd"`
	Meta                *OperationsDataMeta `json:"meta,omitempty"`
}

type DailyCheckinAdminRecord struct {
	ID                int64                       `json:"id"`
	UserID            int64                       `json:"user_id"`
	Username          string                      `json:"username"`
	Email             string                      `json:"email"`
	Date              string                      `json:"date"`
	RewardAmount      float64                     `json:"reward_amount"`
	QualifiedUsageUSD float64                     `json:"qualified_usage_usd"`
	RewardMetadata    *DailyCheckinRewardMetadata `json:"reward_metadata,omitempty"`
	CreatedAt         time.Time                   `json:"created_at"`
}

type DailyCheckinAdminRecordFilter struct {
	Page       int
	PageSize   int
	DateFrom   string
	DateTo     string
	UserQuery  string
	RewardMin  *float64
	RewardMax  *float64
	CritHit    *bool
	StreakDays *int
}

type DailyCheckinAdminRecordList struct {
	Items    []DailyCheckinAdminRecord `json:"items"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

type operationsDateBucket struct {
	Date  string
	Start time.Time
	End   time.Time
}

type DailyCheckinAdminRecordIterator struct {
	rows *sql.Rows
}

func (it *DailyCheckinAdminRecordIterator) Close() error {
	if it == nil || it.rows == nil {
		return nil
	}
	return it.rows.Close()
}

func (it *DailyCheckinAdminRecordIterator) Next() (*DailyCheckinAdminRecord, error) {
	if it == nil || it.rows == nil {
		return nil, nil
	}
	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate daily checkin export records: %w", err)
		}
		return nil, nil
	}
	var item DailyCheckinAdminRecord
	if err := it.rows.Scan(&item.ID, &item.UserID, &item.Username, &item.Email, &item.Date, &item.RewardAmount, &item.QualifiedUsageUSD, newRewardMetadataScanner(&item.RewardMetadata), &item.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan daily checkin export record: %w", err)
	}
	return &item, nil
}

type OperationsOverviewPoint struct {
	Date         string  `json:"date"`
	DAU          int64   `json:"dau"`
	NewUsers     int64   `json:"new_users"`
	RequestUsers int64   `json:"request_users"`
	Requests     int64   `json:"requests"`
	ActualCost   float64 `json:"actual_cost"`
}

type OperationsOverviewSummary struct {
	DAU          int64   `json:"dau"`
	LastDayDAU   int64   `json:"last_day_dau"`
	NewUsers     int64   `json:"new_users"`
	RequestUsers int64   `json:"request_users"`
	PeriodUsers  int64   `json:"period_request_users"`
	Requests     int64   `json:"requests"`
	ActualCost   float64 `json:"actual_cost"`
}

type OperationsOverviewResponse struct {
	Summary OperationsOverviewSummary `json:"summary"`
	Points  []OperationsOverviewPoint `json:"points"`
	Meta    OperationsDataMeta        `json:"meta"`
}

type OperationsRetentionSummary struct {
	CohortUsers       int64   `json:"cohort_users"`
	D1EligibleUsers   int64   `json:"d1_eligible_users"`
	D7EligibleUsers   int64   `json:"d7_eligible_users"`
	D30EligibleUsers  int64   `json:"d30_eligible_users"`
	D1Users           int64   `json:"d1_users"`
	D7Users           int64   `json:"d7_users"`
	D30Users          int64   `json:"d30_users"`
	D1Rate            float64 `json:"d1_rate"`
	D7Rate            float64 `json:"d7_rate"`
	D30Rate           float64 `json:"d30_rate"`
	AverageActiveDays float64 `json:"average_active_days"`
}

type OperationsRetentionResponse struct {
	Summary OperationsRetentionSummary `json:"summary"`
	Meta    OperationsDataMeta         `json:"meta"`
}

type OperationsDataMeta struct {
	Timezone       string   `json:"timezone"`
	AsOf           string   `json:"as_of"`
	RequestedStart string   `json:"requested_start,omitempty"`
	RequestedEnd   string   `json:"requested_end,omitempty"`
	CoverageStart  string   `json:"coverage_start,omitempty"`
	CoverageEnd    string   `json:"coverage_end,omitempty"`
	DataQuality    string   `json:"data_quality"`
	Source         string   `json:"source"`
	Stale          bool     `json:"stale"`
	Warnings       []string `json:"warnings,omitempty"`
}

type DailyCheckinAnalyticsPoint struct {
	Date             string  `json:"date"`
	QualifiedUsers   int64   `json:"qualified_users"`
	CheckinUsers     int64   `json:"checkin_users"`
	CheckinRate      float64 `json:"checkin_rate"`
	RewardUSD        float64 `json:"reward_usd"`
	AverageRewardUSD float64 `json:"avg_reward_usd"`
	FallbackCount    int64   `json:"fallback_count"`
	CritCount        int64   `json:"crit_count"`
	StreakUserCount  int64   `json:"streak_user_count"`
}

type DailyCheckinRewardDistributionItem struct {
	Label     string  `json:"label"`
	Count     int64   `json:"count"`
	RewardUSD float64 `json:"reward_usd"`
}

type DailyCheckinAnalyticsSummary struct {
	QualifiedUsers      int64    `json:"qualified_users"`
	CheckinUsers        int64    `json:"checkin_users"`
	StreakUsers         int64    `json:"streak_users"`
	CheckinRate         float64  `json:"checkin_rate"`
	RewardUSD           float64  `json:"reward_usd"`
	AverageRewardUSD    float64  `json:"avg_reward_usd"`
	FallbackRate        float64  `json:"fallback_rate"`
	CritRate            float64  `json:"crit_rate"`
	StreakUserRate      float64  `json:"streak_user_rate"`
	QualifiedUserDays   int64    `json:"qualified_user_days"`
	CheckinUserDays     int64    `json:"checkin_user_days"`
	OpportunityRate     float64  `json:"checkin_opportunity_rate"`
	DailyRemainingUSD   float64  `json:"daily_remaining_usd"`
	DailyRemainingRate  *float64 `json:"daily_remaining_rate"`
	EstimatedCheckins   *float64 `json:"estimated_remaining_checkins"`
	MonthlyRemainingUSD float64  `json:"monthly_remaining_usd"`
	ProjectedBudgetDays *float64 `json:"projected_budget_days"`
}

type DailyCheckinAnalyticsResponse struct {
	Summary            DailyCheckinAnalyticsSummary         `json:"summary"`
	Points             []DailyCheckinAnalyticsPoint         `json:"points"`
	RewardDistribution []DailyCheckinRewardDistributionItem `json:"reward_distribution"`
	Meta               OperationsDataMeta                   `json:"meta"`
}

type DailyCheckinService struct {
	db                   *sql.DB
	settingService       *SettingService
	billingCacheService  *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

type dailyCheckinQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func NewDailyCheckinService(db *sql.DB, settingService *SettingService, billingCacheService *BillingCacheService, authCacheInvalidator APIKeyAuthCacheInvalidator) *DailyCheckinService {
	return &DailyCheckinService{
		db:                   db,
		settingService:       settingService,
		billingCacheService:  billingCacheService,
		authCacheInvalidator: authCacheInvalidator,
	}
}

func (s *DailyCheckinService) GetStatus(ctx context.Context, userID int64) (*DailyCheckinStatus, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	return s.getStatus(ctx, s.db, userID)
}

func (s *DailyCheckinService) CheckIn(ctx context.Context, userID int64) (*DailyCheckinResult, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}

	now := timezone.Now()
	todayStart := timezone.StartOfDay(now)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	monthStart := timezone.StartOfMonth(now)
	nextMonthStart := monthStart.AddDate(0, 1, 0)
	today := todayStart.Format("2006-01-02")

	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, ErrDailyCheckinDisabled
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin daily checkin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, err := s.getCheckinByDate(ctx, tx, userID, today)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrDailyCheckinAlreadyCheckedIn
	}

	todayUsage, err := sumEligibleUsage(ctx, tx, userID, todayStart, tomorrowStart, settings.UsageScope)
	if err != nil {
		return nil, err
	}
	if todayUsage < settings.RequiredUsageUSD {
		return nil, dailyCheckinUsageNotEnoughError(todayUsage, settings.RequiredUsageUSD)
	}

	reward, err := chooseDailyCheckinReward(ctx, tx, userID, todayStart, tomorrowStart, monthStart, nextMonthStart, settings)
	if err != nil {
		return nil, err
	}
	rewardAmount := reward.Metadata.FinalRewardAmount
	reward.Metadata.RequiredUsageUSD = settings.RequiredUsageUSD
	reward.Metadata.UsageScope = normalizeDailyCheckinUsageScope(settings.UsageScope)
	reward.Metadata.RuleEffectiveAt = now.UTC().Format(time.RFC3339Nano)
	metadataJSON, err := json.Marshal(reward.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal daily checkin reward metadata: %w", err)
	}

	var checkedInAt time.Time
	var checkinID int64
	insertErr := tx.QueryRowContext(ctx, `
			INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at, reward_metadata)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, created_at
		`, userID, today, rewardAmount, todayUsage, now, string(metadataJSON)).Scan(&checkinID, &checkedInAt)
	if insertErr != nil {
		if isDailyCheckinDuplicateError(insertErr) {
			return nil, ErrDailyCheckinAlreadyCheckedIn
		}
		return nil, fmt.Errorf("insert daily checkin: %w", insertErr)
	}

	var balance float64
	if err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance + $1, updated_at = $2
		WHERE id = $3 AND deleted_at IS NULL
		RETURNING balance
	`, rewardAmount, now, userID).Scan(&balance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("USER_NOT_FOUND", "user not found")
		}
		return nil, fmt.Errorf("update daily checkin user balance: %w", err)
	}

	redeemCodeID, err := insertDailyCheckinBalanceRecord(ctx, tx, userID, rewardAmount, now)
	if err != nil {
		return nil, err
	}
	if err := linkDailyCheckinBalanceRecord(ctx, tx, checkinID, redeemCodeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit daily checkin: %w", err)
	}

	s.invalidateUserCaches(ctx, userID)

	status, err := s.getStatus(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	return &DailyCheckinResult{
		DailyCheckinStatus:  *status,
		RewardAmount:        rewardAmount,
		BaseRewardAmount:    reward.Metadata.BaseRewardAmount,
		Message:             reward.Metadata.BudgetFallbackText,
		BudgetFallback:      reward.Metadata.BudgetFallback,
		StreakDays:          reward.Metadata.StreakDays,
		StreakMultiplier:    reward.Metadata.StreakMultiplier,
		CritHit:             reward.Metadata.CritHit,
		CritMultiplier:      reward.Metadata.CritMultiplier,
		PreCritRewardAmount: reward.Metadata.PreCritRewardAmount,
		Balance:             balance,
		CheckedInAt:         checkedInAt,
	}, nil
}

func (s *DailyCheckinService) GetAdminStats(ctx context.Context, _ string) (*DailyCheckinAdminStats, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	// Budget enforcement and user_checkins.checkin_date both use the configured
	// server check-in timezone. Keep administrative budget figures on the same
	// logical calendar even when the browser requests a different report zone.
	now := timezone.Now()
	todayStart := timezone.StartOfDay(now)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	monthStart := timezone.StartOfMonth(now)
	nextMonthStart := monthStart.AddDate(0, 1, 0)

	todayCount, todayUsers, todayReward, err := aggregateCheckinStats(ctx, s.db, todayStart.Format("2006-01-02"), tomorrowStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	monthCount, monthUsers, monthReward, err := aggregateCheckinStats(ctx, s.db, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	averageReward := 0.0
	if monthCount > 0 {
		averageReward = monthReward / float64(monthCount)
	}
	return &DailyCheckinAdminStats{
		Enabled:             settings.Enabled,
		RequiredUsageUSD:    settings.RequiredUsageUSD,
		UsageScope:          settings.UsageScope,
		RewardMinUSD:        settings.RewardMinUSD,
		RewardMaxUSD:        settings.RewardMaxUSD,
		TodayCheckins:       todayCount,
		TodayUsers:          todayUsers,
		TodayRewardUSD:      todayReward,
		MonthCheckins:       monthCount,
		MonthUsers:          monthUsers,
		MonthRewardUSD:      monthReward,
		AverageRewardUSD:    averageReward,
		DailyBudgetUSD:      settings.DailyBudgetUSD,
		DailyRemainingUSD:   remainingBudget(settings.DailyBudgetUSD, todayReward),
		MonthlyBudgetUSD:    settings.MonthlyBudgetUSD,
		MonthlyRemainingUSD: remainingBudget(settings.MonthlyBudgetUSD, monthReward),
		UserMonthlyLimitUSD: settings.UserMonthlyLimitUSD,
		Meta: &OperationsDataMeta{
			Timezone:       now.Location().String(),
			AsOf:           now.UTC().Format(time.RFC3339Nano),
			RequestedStart: monthStart.Format("2006-01-02"),
			RequestedEnd:   todayStart.Format("2006-01-02"),
			CoverageStart:  monthStart.Format("2006-01-02"),
			CoverageEnd:    todayStart.Format("2006-01-02"),
			DataQuality:    "complete",
			Source:         "user_checkins",
		},
	}, nil
}

func (s *DailyCheckinService) ListAdminRecords(ctx context.Context, filter DailyCheckinAdminRecordFilter) (*DailyCheckinAdminRecordList, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	where, args := buildDailyCheckinRecordWhere(filter)
	var total int64
	countQuery := `SELECT COUNT(*) FROM user_checkins c JOIN users u ON u.id = c.user_id ` + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count daily checkin records: %w", err)
	}

	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	query := fmt.Sprintf(`
		SELECT c.id, c.user_id, COALESCE(u.username, ''), COALESCE(u.email, ''), c.checkin_date,
		       c.reward_amount, c.qualified_usage_usd, c.reward_metadata, c.created_at
		FROM user_checkins c
		JOIN users u ON u.id = c.user_id
		%s
		ORDER BY c.checkin_date DESC, c.id DESC
		LIMIT $%d OFFSET $%d
	`, where, limitArg, offsetArg)
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list daily checkin records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]DailyCheckinAdminRecord, 0, filter.PageSize)
	for rows.Next() {
		var item DailyCheckinAdminRecord
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.Email, &item.Date, &item.RewardAmount, &item.QualifiedUsageUSD, newRewardMetadataScanner(&item.RewardMetadata), &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan daily checkin admin record: %w", err)
		}
		item.Email = maskOperationsEmail(item.Email)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily checkin admin records: %w", err)
	}
	return &DailyCheckinAdminRecordList{
		Items:    items,
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}, nil
}

func maskOperationsEmail(email string) string {
	parts := strings.SplitN(strings.TrimSpace(email), "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	local := []rune(parts[0])
	if len(local) == 1 {
		return string(local[0]) + "***@" + parts[1]
	}
	return string(local[0]) + "***" + string(local[len(local)-1]) + "@" + parts[1]
}

func (s *DailyCheckinService) ExportAdminRecords(ctx context.Context, filter DailyCheckinAdminRecordFilter) ([]DailyCheckinAdminRecord, error) {
	items := make([]DailyCheckinAdminRecord, 0)
	err := s.ForEachAdminRecord(ctx, filter, func(item DailyCheckinAdminRecord) error {
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *DailyCheckinService) OpenAdminRecordIterator(ctx context.Context, filter DailyCheckinAdminRecordFilter) (*DailyCheckinAdminRecordIterator, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	where, args := buildDailyCheckinRecordWhere(filter)
	query := fmt.Sprintf(`
		SELECT c.id, c.user_id, COALESCE(u.username, ''), COALESCE(u.email, ''), c.checkin_date,
		       c.reward_amount, c.qualified_usage_usd, c.reward_metadata, c.created_at
		FROM user_checkins c
		JOIN users u ON u.id = c.user_id
		%s
		ORDER BY c.checkin_date DESC, c.id DESC
	`, where)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("export daily checkin records: %w", err)
	}
	return &DailyCheckinAdminRecordIterator{rows: rows}, nil
}

func (s *DailyCheckinService) ForEachAdminRecord(ctx context.Context, filter DailyCheckinAdminRecordFilter, consume func(DailyCheckinAdminRecord) error) error {
	if consume == nil {
		return infraerrors.InternalServer("DAILY_CHECKIN_EXPORT_CONSUMER_MISSING", "daily check-in export consumer is unavailable")
	}
	iter, err := s.OpenAdminRecordIterator(ctx, filter)
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()
	for {
		item, err := iter.Next()
		if err != nil {
			return err
		}
		if item == nil {
			return nil
		}
		if err := consume(*item); err != nil {
			return err
		}
	}
}

func (s *DailyCheckinService) GetOperationsOverview(ctx context.Context, start, end time.Time) (*OperationsOverviewResponse, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	points, dayIndex, buckets := newOperationsOverviewPoints(start, end)
	dailyRequestUsers := make([]map[int64]struct{}, len(points))
	dailyNewUsers := make([]map[int64]struct{}, len(points))
	rangeRequestUsers := map[int64]struct{}{}
	rangeNewUsers := map[int64]struct{}{}
	summary := OperationsOverviewSummary{}

	usageByDate, err := s.operationsOverviewUsageByDate(ctx, buckets)
	if err != nil {
		return nil, err
	}
	newUsersByDate, err := s.operationsOverviewNewUsersByDate(ctx, buckets)
	if err != nil {
		return nil, err
	}
	for _, bucket := range buckets {
		idx, ok := dayIndex[bucket.Date]
		if !ok {
			continue
		}
		if dailyRequestUsers[idx] == nil {
			dailyRequestUsers[idx] = map[int64]struct{}{}
		}
		if dailyNewUsers[idx] == nil {
			dailyNewUsers[idx] = map[int64]struct{}{}
		}
		usage := usageByDate[bucket.Date]
		for userID := range usage.Users {
			dailyRequestUsers[idx][userID] = struct{}{}
			rangeRequestUsers[userID] = struct{}{}
		}
		for userID := range newUsersByDate[bucket.Date] {
			dailyNewUsers[idx][userID] = struct{}{}
			rangeNewUsers[userID] = struct{}{}
		}
		points[idx].RequestUsers = int64(len(dailyRequestUsers[idx]))
		points[idx].DAU = points[idx].RequestUsers
		points[idx].NewUsers = int64(len(dailyNewUsers[idx]))
		points[idx].Requests = usage.Requests
		points[idx].ActualCost = usage.ActualCost
		summary.Requests += usage.Requests
		summary.ActualCost += usage.ActualCost
	}
	summary.RequestUsers = int64(len(rangeRequestUsers))
	summary.NewUsers = int64(len(rangeNewUsers))
	if len(points) > 0 {
		summary.DAU = points[len(points)-1].DAU
	}
	summary.LastDayDAU = summary.DAU
	summary.PeriodUsers = summary.RequestUsers
	return &OperationsOverviewResponse{Summary: summary, Points: points, Meta: s.operationsDataMeta(ctx, start, end)}, nil
}

func (s *DailyCheckinService) GetOperationsRetention(ctx context.Context, start, end time.Time) (*OperationsRetentionResponse, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	cohortRows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at
		FROM users
		WHERE created_at >= $1 AND created_at < $2 AND deleted_at IS NULL
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("list retention cohort users: %w", err)
	}
	defer func() { _ = cohortRows.Close() }()
	cohortDates := map[int64]time.Time{}
	for cohortRows.Next() {
		var userID int64
		var createdAt time.Time
		if err := cohortRows.Scan(&userID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan retention cohort user: %w", err)
		}
		cohortDates[userID] = calendarDay(createdAt.In(start.Location()))
	}
	if err := cohortRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention cohort users: %w", err)
	}

	activityEnd := end.AddDate(0, 0, 30)
	activities, err := s.listOperationsRetentionActivity(ctx, start, activityEnd)
	if err != nil {
		return nil, err
	}
	activeDays := make(map[int64]map[string]struct{}, len(cohortDates))
	d1 := map[int64]struct{}{}
	d7 := map[int64]struct{}{}
	d30 := map[int64]struct{}{}
	for _, activity := range activities {
		cohortDate, ok := cohortDates[activity.UserID]
		if !ok {
			continue
		}
		activityDate := calendarDay(activity.Date.In(start.Location()))
		offset := int(activityDate.Sub(cohortDate).Hours() / 24)
		if offset < 0 {
			continue
		}
		if activeDays[activity.UserID] == nil {
			activeDays[activity.UserID] = map[string]struct{}{}
		}
		activeDays[activity.UserID][activityDate.Format("2006-01-02")] = struct{}{}
		switch offset {
		case 1:
			d1[activity.UserID] = struct{}{}
		case 7:
			d7[activity.UserID] = struct{}{}
		case 30:
			d30[activity.UserID] = struct{}{}
		}
	}

	summary := OperationsRetentionSummary{
		CohortUsers: int64(len(cohortDates)),
	}
	today := calendarDay(time.Now().In(start.Location()))
	for userID, cohortDate := range cohortDates {
		if !cohortDate.AddDate(0, 0, 2).After(today) {
			summary.D1EligibleUsers++
			if _, ok := d1[userID]; ok {
				summary.D1Users++
			}
		}
		if !cohortDate.AddDate(0, 0, 8).After(today) {
			summary.D7EligibleUsers++
			if _, ok := d7[userID]; ok {
				summary.D7Users++
			}
		}
		if !cohortDate.AddDate(0, 0, 31).After(today) {
			summary.D30EligibleUsers++
			if _, ok := d30[userID]; ok {
				summary.D30Users++
			}
		}
	}
	if summary.CohortUsers > 0 {
		var totalActiveDays int
		for userID := range cohortDates {
			totalActiveDays += len(activeDays[userID])
		}
		summary.AverageActiveDays = float64(totalActiveDays) / float64(summary.CohortUsers)
	}
	if summary.D1EligibleUsers > 0 {
		summary.D1Rate = float64(summary.D1Users) / float64(summary.D1EligibleUsers)
	}
	if summary.D7EligibleUsers > 0 {
		summary.D7Rate = float64(summary.D7Users) / float64(summary.D7EligibleUsers)
	}
	if summary.D30EligibleUsers > 0 {
		summary.D30Rate = float64(summary.D30Users) / float64(summary.D30EligibleUsers)
	}
	meta := s.operationsDataMeta(ctx, start, activityEnd)
	meta.RequestedEnd = end.AddDate(0, 0, -1).Format("2006-01-02")
	if summary.D1EligibleUsers < summary.CohortUsers || summary.D7EligibleUsers < summary.CohortUsers || summary.D30EligibleUsers < summary.CohortUsers {
		meta.DataQuality = "partial"
		meta.Warnings = append(meta.Warnings, "retention_cohorts_not_fully_matured")
	}
	return &OperationsRetentionResponse{Summary: summary, Meta: meta}, nil
}

func calendarDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

type operationsRetentionActivity struct {
	UserID int64
	Date   time.Time
}

func (s *DailyCheckinService) listOperationsRetentionActivity(ctx context.Context, start, end time.Time) ([]operationsRetentionActivity, error) {
	if operationsShouldUseDailyAggregatesForRange(start, end) {
		coveredFrom, coveredTo, ok, err := s.operationsDailyAggregateCoverage(ctx)
		if err != nil {
			return nil, err
		}
		if ok && !start.Before(coveredFrom) && !end.After(coveredTo) {
			rows, err := s.db.QueryContext(ctx, `
				SELECT user_id, CAST(bucket_date AS TEXT)
				FROM usage_dashboard_daily_user_stats
				WHERE bucket_date >= $1 AND bucket_date < $2
				GROUP BY user_id, bucket_date
			`, start.Format("2006-01-02"), end.Format("2006-01-02"))
			if err == nil {
				defer func() { _ = rows.Close() }()
				activities := make([]operationsRetentionActivity, 0)
				for rows.Next() {
					var activity operationsRetentionActivity
					var date string
					if err := rows.Scan(&activity.UserID, &date); err != nil {
						return nil, fmt.Errorf("scan retention daily aggregate: %w", err)
					}
					activity.Date, err = time.ParseInLocation("2006-01-02", date, start.Location())
					if err != nil {
						return nil, fmt.Errorf("parse retention daily aggregate date: %w", err)
					}
					activities = append(activities, activity)
				}
				if err := rows.Err(); err != nil {
					return nil, fmt.Errorf("iterate retention daily aggregates: %w", err)
				}
				return activities, nil
			}
			if !isOperationsAggregateUnavailable(err) {
				return nil, fmt.Errorf("list retention daily aggregates: %w", err)
			}
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, created_at
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("list retention activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	activities := make([]operationsRetentionActivity, 0)
	for rows.Next() {
		var activity operationsRetentionActivity
		if err := rows.Scan(&activity.UserID, &activity.Date); err != nil {
			return nil, fmt.Errorf("scan retention activity: %w", err)
		}
		activities = append(activities, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention activity: %w", err)
	}
	return activities, nil
}

type operationsUsageAggregate struct {
	Users      map[int64]struct{}
	Requests   int64
	ActualCost float64
}

func (s *DailyCheckinService) operationsOverviewUsageByDate(ctx context.Context, buckets []operationsDateBucket) (map[string]operationsUsageAggregate, error) {
	if operationsShouldUseDailyAggregates(buckets) {
		aggregates, covered, err := s.operationsOverviewUsageByDateFromDailyAggregates(ctx, buckets)
		if err != nil {
			return nil, err
		}
		if covered {
			return aggregates, nil
		}
	}
	return s.operationsOverviewUsageByDateFromUsageLogs(ctx, buckets)
}

func (s *DailyCheckinService) operationsOverviewUsageByDateFromUsageLogs(ctx context.Context, buckets []operationsDateBucket) (map[string]operationsUsageAggregate, error) {
	query, args := buildUsageBucketAggregateQuery(buckets, "")
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate operations overview usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	aggregates := map[string]operationsUsageAggregate{}
	for rows.Next() {
		var date string
		var userID int64
		var userRequests int64
		var userActualCost float64
		if err := rows.Scan(&date, &userID, &userRequests, &userActualCost); err != nil {
			return nil, fmt.Errorf("scan operations overview usage aggregate: %w", err)
		}
		aggregate := aggregates[date]
		if aggregate.Users == nil {
			aggregate.Users = map[int64]struct{}{}
		}
		aggregate.Users[userID] = struct{}{}
		aggregate.Requests += userRequests
		aggregate.ActualCost += userActualCost
		aggregates[date] = aggregate
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operations overview usage aggregate: %w", err)
	}
	return aggregates, nil
}

func (s *DailyCheckinService) operationsOverviewUsageByDateFromDailyAggregates(ctx context.Context, buckets []operationsDateBucket) (map[string]operationsUsageAggregate, bool, error) {
	coveredFrom, coveredTo, ok, err := s.operationsDailyAggregateCoverage(ctx)
	if err != nil || !ok {
		return nil, false, err
	}
	aggregateBuckets, rawTailBuckets := splitOperationsAggregateBuckets(buckets, coveredFrom, coveredTo, nil)
	if len(aggregateBuckets) == 0 {
		return nil, false, nil
	}

	aggregates := make(map[string]operationsUsageAggregate, len(buckets))
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(bucket_date AS TEXT), user_id, COALESCE(total_requests, 0), COALESCE(actual_cost, 0)
		FROM usage_dashboard_daily_user_stats
		WHERE bucket_date >= $1 AND bucket_date < $2
		ORDER BY bucket_date ASC, user_id ASC
	`, aggregateBuckets[0].Date, aggregateBuckets[len(aggregateBuckets)-1].End.Format("2006-01-02"))
	if err != nil {
		if isOperationsAggregateUnavailable(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("aggregate operations overview from daily stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var date string
		var userID int64
		var requests int64
		var actualCost float64
		if err := rows.Scan(&date, &userID, &requests, &actualCost); err != nil {
			return nil, false, fmt.Errorf("scan operations daily aggregate: %w", err)
		}
		aggregate := aggregates[date]
		if aggregate.Users == nil {
			aggregate.Users = map[int64]struct{}{}
		}
		aggregate.Users[userID] = struct{}{}
		aggregate.Requests += requests
		aggregate.ActualCost += actualCost
		aggregates[date] = aggregate
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate operations daily aggregates: %w", err)
	}

	if len(rawTailBuckets) > 0 {
		tail, err := s.operationsOverviewUsageByDateFromUsageLogs(ctx, rawTailBuckets)
		if err != nil {
			return nil, false, err
		}
		for date, aggregate := range tail {
			aggregates[date] = aggregate
		}
	}
	return aggregates, true, nil
}

func (s *DailyCheckinService) operationsOverviewNewUsersByDate(ctx context.Context, buckets []operationsDateBucket) (map[string]map[int64]struct{}, error) {
	query, args := buildNewUsersBucketQuery(buckets)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list operations overview new users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make(map[string]map[int64]struct{}, len(buckets))
	for _, bucket := range buckets {
		items[bucket.Date] = map[int64]struct{}{}
	}
	for rows.Next() {
		var date string
		var userID int64
		if err := rows.Scan(&date, &userID); err != nil {
			return nil, fmt.Errorf("scan operations overview new user: %w", err)
		}
		if items[date] == nil {
			items[date] = map[int64]struct{}{}
		}
		items[date][userID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operations overview new users: %w", err)
	}
	return items, nil
}

func newOperationsOverviewPoints(start, end time.Time) ([]OperationsOverviewPoint, map[string]int, []operationsDateBucket) {
	points := make([]OperationsOverviewPoint, 0)
	index := map[string]int{}
	buckets := make([]operationsDateBucket, 0)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		next := day.AddDate(0, 0, 1)
		date := day.Format("2006-01-02")
		index[date] = len(points)
		points = append(points, OperationsOverviewPoint{Date: date})
		buckets = append(buckets, operationsDateBucket{Date: date, Start: day, End: next})
	}
	return points, index, buckets
}

func buildNewUsersBucketQuery(buckets []operationsDateBucket) (string, []any) {
	if len(buckets) == 0 {
		return `
			SELECT '' AS bucket_date, id
			FROM users
			WHERE 1 = 0
		`, nil
	}
	parts := make([]string, 0, len(buckets))
	args := make([]any, 0, len(buckets)*3)
	for _, bucket := range buckets {
		dateArg := len(args) + 1
		startArg := dateArg + 1
		endArg := dateArg + 2
		parts = append(parts, fmt.Sprintf(`
			SELECT CAST($%d AS TEXT) AS bucket_date, id
			FROM users
			WHERE created_at >= $%d AND created_at < $%d AND deleted_at IS NULL
		`, dateArg, startArg, endArg))
		args = append(args, bucket.Date, bucket.Start, bucket.End)
	}
	return strings.Join(parts, "\nUNION ALL\n"), args
}

func buildUsageBucketAggregateQuery(buckets []operationsDateBucket, extraWhere string) (string, []any) {
	if len(buckets) == 0 {
		return `
			SELECT '' AS bucket_date, user_id, COUNT(*), COALESCE(SUM(actual_cost), 0)
			FROM usage_logs
			WHERE 1 = 0
			GROUP BY user_id
		`, nil
	}
	parts := make([]string, 0, len(buckets))
	args := make([]any, 0, len(buckets)*3)
	for _, bucket := range buckets {
		dateArg := len(args) + 1
		startArg := dateArg + 1
		endArg := dateArg + 2
		parts = append(parts, fmt.Sprintf(`
			SELECT CAST($%d AS TEXT) AS bucket_date, user_id, COUNT(*), COALESCE(SUM(actual_cost), 0)
			FROM usage_logs
			WHERE created_at >= $%d AND created_at < $%d%s
			GROUP BY user_id
		`, dateArg, startArg, endArg, extraWhere))
		args = append(args, bucket.Date, bucket.Start, bucket.End)
	}
	return strings.Join(parts, "\nUNION ALL\n"), args
}

func (s *DailyCheckinService) GetDailyCheckinAnalytics(ctx context.Context, start, end time.Time) (*DailyCheckinAnalyticsResponse, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}

	checkins, err := s.listCheckinsForAnalytics(ctx, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	ruleHistory, err := s.dailyCheckinRuleHistory(ctx, settings)
	if err != nil {
		return nil, err
	}
	qualifiedByDate, err := s.qualifiedUsersByDate(ctx, start, end, ruleHistory)
	if err != nil {
		return nil, err
	}
	byDate := make(map[string][]DailyCheckinAdminRecord)
	distribution := map[string]*DailyCheckinRewardDistributionItem{}
	streakUserSet := map[int64]struct{}{}
	var totalCheckins, fallbackCount, critCount int64
	var rewardUSD float64
	for _, item := range checkins {
		byDate[item.Date] = append(byDate[item.Date], item)
		totalCheckins++
		rewardUSD += item.RewardAmount
		label := rewardDistributionLabel(item.RewardAmount)
		bucket := distribution[label]
		if bucket == nil {
			bucket = &DailyCheckinRewardDistributionItem{Label: label}
			distribution[label] = bucket
		}
		bucket.Count++
		bucket.RewardUSD += item.RewardAmount
		if item.RewardMetadata != nil {
			if item.RewardMetadata.BudgetFallback {
				fallbackCount++
			}
			if item.RewardMetadata.CritHit {
				critCount++
			}
			if item.RewardMetadata.StreakDays > 1 {
				streakUserSet[item.UserID] = struct{}{}
			}
		}
	}

	points := make([]DailyCheckinAnalyticsPoint, 0)
	qualifiedUserSet := map[int64]struct{}{}
	checkinUserSet := map[int64]struct{}{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		qualifiedIDs := qualifiedByDate[date]
		records := byDate[date]
		if qualifiedIDs == nil {
			qualifiedIDs = map[int64]struct{}{}
			qualifiedByDate[date] = qualifiedIDs
		}
		for _, item := range records {
			qualifiedIDs[item.UserID] = struct{}{}
		}
		qualifiedUsers := int64(len(qualifiedIDs))
		for userID := range qualifiedIDs {
			qualifiedUserSet[userID] = struct{}{}
		}
		point := DailyCheckinAnalyticsPoint{
			Date:           date,
			QualifiedUsers: qualifiedUsers,
			CheckinUsers:   int64(len(records)),
		}
		for _, item := range records {
			checkinUserSet[item.UserID] = struct{}{}
			point.RewardUSD += item.RewardAmount
			if item.RewardMetadata != nil {
				if item.RewardMetadata.BudgetFallback {
					point.FallbackCount++
				}
				if item.RewardMetadata.CritHit {
					point.CritCount++
				}
				if item.RewardMetadata.StreakDays > 1 {
					point.StreakUserCount++
				}
			}
		}
		if point.CheckinUsers > 0 {
			point.AverageRewardUSD = point.RewardUSD / float64(point.CheckinUsers)
		}
		if point.QualifiedUsers > 0 {
			point.CheckinRate = float64(point.CheckinUsers) / float64(point.QualifiedUsers)
		}
		points = append(points, point)
	}

	now := time.Now().In(start.Location())
	todayStart := startOfDayInLocation(now)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	nextMonthStart := monthStart.AddDate(0, 1, 0)
	_, _, todayReward, err := aggregateCheckinStats(ctx, s.db, todayStart.Format("2006-01-02"), tomorrowStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	_, _, monthReward, err := aggregateCheckinStats(ctx, s.db, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}

	summary := DailyCheckinAnalyticsSummary{
		QualifiedUsers:      int64(len(qualifiedUserSet)),
		CheckinUsers:        int64(len(checkinUserSet)),
		StreakUsers:         int64(len(streakUserSet)),
		RewardUSD:           rewardUSD,
		DailyRemainingUSD:   remainingBudget(settings.DailyBudgetUSD, todayReward),
		MonthlyRemainingUSD: remainingBudget(settings.MonthlyBudgetUSD, monthReward),
	}
	if summary.QualifiedUsers > 0 {
		summary.CheckinRate = float64(summary.CheckinUsers) / float64(summary.QualifiedUsers)
	}
	if totalCheckins > 0 {
		summary.AverageRewardUSD = rewardUSD / float64(totalCheckins)
		summary.FallbackRate = float64(fallbackCount) / float64(totalCheckins)
		summary.CritRate = float64(critCount) / float64(totalCheckins)
	}
	if summary.CheckinUsers > 0 {
		summary.StreakUserRate = float64(summary.StreakUsers) / float64(summary.CheckinUsers)
	}
	for _, point := range points {
		summary.QualifiedUserDays += point.QualifiedUsers
		summary.CheckinUserDays += point.CheckinUsers
	}
	if summary.QualifiedUserDays > 0 {
		summary.OpportunityRate = float64(summary.CheckinUserDays) / float64(summary.QualifiedUserDays)
	}
	applyProjectedBudgetMetrics(&summary, points, settings)

	dist := make([]DailyCheckinRewardDistributionItem, 0, len(distribution))
	for _, item := range distribution {
		dist = append(dist, *item)
	}
	sortRewardDistribution(dist)
	return &DailyCheckinAnalyticsResponse{Summary: summary, Points: points, RewardDistribution: dist, Meta: s.operationsDataMeta(ctx, start, end)}, nil
}

func (s *DailyCheckinService) getStatus(ctx context.Context, q dailyCheckinQuerier, userID int64) (*DailyCheckinStatus, error) {
	now := timezone.Now()
	todayStart := timezone.StartOfDay(now)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	monthStart := timezone.StartOfMonth(now)
	nextMonthStart := monthStart.AddDate(0, 1, 0)

	today := todayStart.Format("2006-01-02")
	month := monthStart.Format("2006-01")

	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
	todayUsage, err := sumEligibleUsage(ctx, q, userID, todayStart, tomorrowStart, settings.UsageScope)
	if err != nil {
		return nil, err
	}
	records, err := listMonthlyCheckins(ctx, q, userID, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	checkedIn := false
	for _, record := range records {
		if record.Date == today {
			checkedIn = true
			break
		}
	}
	userMonthlyReward := 0.0
	if settings.UserMonthlyLimitUSD > 0 {
		userMonthlyReward, err = sumCheckinRewards(ctx, q, userID, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}
	dailyReward := 0.0
	if settings.DailyBudgetUSD > 0 {
		dailyReward, err = sumCheckinRewards(ctx, q, 0, todayStart.Format("2006-01-02"), tomorrowStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}
	monthlyReward := 0.0
	if settings.MonthlyBudgetUSD > 0 {
		monthlyReward, err = sumCheckinRewards(ctx, q, 0, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}
	budgetExhausted := isDailyCheckinBudgetExhausted(settings.RewardMinUSD, dailyReward, monthlyReward, userMonthlyReward, settings)
	fallbackAvailable := budgetExhausted && canUseDailyCheckinBudgetFallback(settings.BudgetFallbackUSD, dailyReward, settings)

	return &DailyCheckinStatus{
		Enabled:              settings.Enabled,
		Today:                today,
		Month:                month,
		CheckedIn:            checkedIn,
		Eligible:             settings.Enabled && todayUsage >= settings.RequiredUsageUSD && !checkedIn && (!budgetExhausted || fallbackAvailable),
		TodayUsageUSD:        todayUsage,
		RequiredUsageUSD:     settings.RequiredUsageUSD,
		UsageScope:           settings.UsageScope,
		RewardMinUSD:         settings.RewardMinUSD,
		RewardMaxUSD:         settings.RewardMaxUSD,
		DailyBudgetUSD:       settings.DailyBudgetUSD,
		DailyRewardUSD:       dailyReward,
		MonthlyBudgetUSD:     settings.MonthlyBudgetUSD,
		MonthlyRewardUSD:     monthlyReward,
		UserMonthlyLimitUSD:  settings.UserMonthlyLimitUSD,
		UserMonthlyRewardUSD: userMonthlyReward,
		BudgetExhausted:      budgetExhausted,
		MonthCheckins:        records,
	}, nil
}

func (s *DailyCheckinService) settings(ctx context.Context) (*DailyCheckinSettings, error) {
	if s == nil || s.settingService == nil {
		return &DailyCheckinSettings{
			Enabled:            true,
			RequiredUsageUSD:   DailyCheckinRequiredUsageDefault,
			UsageScope:         DailyCheckinUsageScopeActualCost,
			RewardMinUSD:       DailyCheckinRewardMinDefault,
			RewardMaxUSD:       DailyCheckinRewardMaxDefault,
			BudgetFallbackUSD:  DailyCheckinBudgetFallbackDefault,
			BudgetFallbackText: DailyCheckinBudgetFallbackText,
		}, nil
	}
	return s.settingService.GetDailyCheckinSettings(ctx)
}

func (s *DailyCheckinService) getCheckinByDate(ctx context.Context, q dailyCheckinQuerier, userID int64, date string) (*DailyCheckinRecord, error) {
	var record DailyCheckinRecord
	var redeemCodeID sql.NullInt64
	err := q.QueryRowContext(ctx, `
				SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, redeem_code_id, created_at, reward_metadata
				FROM user_checkins
				WHERE user_id = $1 AND checkin_date = $2
			`, userID, date).Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &redeemCodeID, &record.CreatedAt, newRewardMetadataScanner(&record.RewardMetadata))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get daily checkin: %w", err)
	}
	if redeemCodeID.Valid {
		record.RedeemCodeID = &redeemCodeID.Int64
	}
	return &record, nil
}

func sumEligibleUsage(ctx context.Context, q dailyCheckinQuerier, userID int64, start, end time.Time, usageScope string) (float64, error) {
	var total float64
	query := `
			SELECT COALESCE(SUM(actual_cost), 0)
			FROM usage_logs
			WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`
	if normalizeDailyCheckinUsageScope(usageScope) == DailyCheckinUsageScopeBalanceOnly {
		query += ` AND subscription_id IS NULL`
	}
	if err := q.QueryRowContext(ctx, query, userID, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum daily eligible usage: %w", err)
	}
	return total, nil
}

func listMonthlyCheckins(ctx context.Context, q dailyCheckinQuerier, userID int64, monthStart, nextMonthStart string) ([]DailyCheckinRecord, error) {
	rows, err := q.QueryContext(ctx, `
			SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, redeem_code_id, created_at, reward_metadata
			FROM user_checkins
			WHERE user_id = $1 AND checkin_date >= $2 AND checkin_date < $3
			ORDER BY checkin_date ASC
	`, userID, monthStart, nextMonthStart)
	if err != nil {
		return nil, fmt.Errorf("list monthly daily checkins: %w", err)
	}
	defer func() { _ = rows.Close() }()

	records := make([]DailyCheckinRecord, 0)
	for rows.Next() {
		var record DailyCheckinRecord
		var redeemCodeID sql.NullInt64
		if err := rows.Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &redeemCodeID, &record.CreatedAt, newRewardMetadataScanner(&record.RewardMetadata)); err != nil {
			return nil, fmt.Errorf("scan daily checkin: %w", err)
		}
		if redeemCodeID.Valid {
			record.RedeemCodeID = &redeemCodeID.Int64
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily checkins: %w", err)
	}
	return records, nil
}

type rewardMetadataScanner struct {
	target **DailyCheckinRewardMetadata
}

func newRewardMetadataScanner(target **DailyCheckinRewardMetadata) *rewardMetadataScanner {
	return &rewardMetadataScanner{target: target}
}

func (s *rewardMetadataScanner) Scan(value any) error {
	if s == nil || s.target == nil {
		return nil
	}
	if value == nil {
		*s.target = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan daily checkin reward metadata: unsupported type %T", value)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*s.target = nil
		return nil
	}
	var metadata DailyCheckinRewardMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return fmt.Errorf("scan daily checkin reward metadata: %w", err)
	}
	*s.target = &metadata
	return nil
}

func insertDailyCheckinBalanceRecord(ctx context.Context, tx *sql.Tx, userID int64, rewardAmount float64, now time.Time) (int64, error) {
	code, err := GenerateRedeemCode()
	if err != nil {
		return 0, infraerrors.InternalServer("DAILY_CHECKIN_BALANCE_RECORD_CODE_FAILED", "failed to generate daily sign-in balance record code").WithCause(err)
	}

	var redeemCodeID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, created_at, validity_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $6, 30)
		RETURNING id
		`, code, RedeemTypeCheckinBalance, rewardAmount, StatusUsed, userID, now, RedeemNotesDailyCheckinReward).Scan(&redeemCodeID); err != nil {
		return 0, fmt.Errorf("insert daily checkin balance record: %w", err)
	}
	return redeemCodeID, nil
}

func linkDailyCheckinBalanceRecord(ctx context.Context, tx *sql.Tx, checkinID, redeemCodeID int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_checkins
		SET redeem_code_id = $1
		WHERE id = $2
	`, redeemCodeID, checkinID); err != nil {
		return fmt.Errorf("link daily checkin balance record: %w", err)
	}
	return nil
}

type dailyCheckinRewardChoice struct {
	Metadata DailyCheckinRewardMetadata
}

func chooseDailyCheckinReward(ctx context.Context, tx *sql.Tx, userID int64, todayStart, tomorrowStart, monthStart, nextMonthStart time.Time, settings *DailyCheckinSettings) (*dailyCheckinRewardChoice, error) {
	if settings == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_SETTINGS_MISSING", "daily check-in settings are missing")
	}
	if hasDailyCheckinBudget(settings) {
		if err := lockDailyCheckinBudget(ctx, tx); err != nil {
			return nil, err
		}
	}

	dailyReward, monthlyReward, userMonthlyReward := 0.0, 0.0, 0.0
	var err error
	if settings.DailyBudgetUSD > 0 {
		dailyReward, err = sumCheckinRewards(ctx, tx, 0, todayStart.Format("2006-01-02"), tomorrowStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}
	if settings.MonthlyBudgetUSD > 0 {
		monthlyReward, err = sumCheckinRewards(ctx, tx, 0, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}
	if settings.UserMonthlyLimitUSD > 0 {
		userMonthlyReward, err = sumCheckinRewards(ctx, tx, userID, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
	}

	baseReward, tier, err := randomDailyCheckinRewardFromTiers(settings)
	if err != nil {
		return nil, err
	}
	streakDays, err := countDailyCheckinStreak(ctx, tx, userID, todayStart, settings)
	if err != nil {
		return nil, err
	}
	streakMultiplier := dailyCheckinStreakMultiplier(streakDays, settings)
	preCritReward := roundDailyCheckinAmount(baseReward * streakMultiplier)
	critEligible := settings.CritEnabled && (settings.CritMaxRewardUSD <= 0 || preCritReward <= settings.CritMaxRewardUSD)
	critHit := false
	critMultiplier := 1.0
	finalReward := preCritReward
	if critEligible {
		critHit, err = randomDailyCheckinPercentHit(settings.CritProbability)
		if err != nil {
			return nil, err
		}
		if critHit {
			critMultiplier = settings.CritMultiplier
			finalReward = roundDailyCheckinAmount(preCritReward * critMultiplier)
		}
	}

	maxAllowed := maxDailyCheckinRewardByBudget(finalReward, dailyReward, monthlyReward, userMonthlyReward, settings)
	if maxAllowed < settings.RewardMinUSD {
		if canUseDailyCheckinBudgetFallback(settings.BudgetFallbackUSD, dailyReward, settings) {
			fallbackReward := normalizeDailyCheckinFallbackReward(settings.BudgetFallbackUSD)
			return &dailyCheckinRewardChoice{Metadata: DailyCheckinRewardMetadata{
				BaseRewardAmount:    fallbackReward,
				BudgetFallback:      true,
				BudgetFallbackText:  normalizeDailyCheckinFallbackText(settings.BudgetFallbackText),
				StreakDays:          streakDays,
				StreakMultiplier:    1,
				CritEligible:        false,
				CritHit:             false,
				CritMultiplier:      1,
				PreCritRewardAmount: fallbackReward,
				FinalRewardAmount:   fallbackReward,
			}}, nil
		}
		return nil, dailyCheckinBudgetExhaustedError(settings.RewardMinUSD, dailyReward, monthlyReward, userMonthlyReward, settings)
	}
	if finalReward > maxAllowed {
		finalReward = roundDailyCheckinAmount(maxAllowed)
	}
	if finalReward <= 0 {
		return nil, dailyCheckinBudgetExhaustedError(settings.RewardMinUSD, dailyReward, monthlyReward, userMonthlyReward, settings)
	}

	return &dailyCheckinRewardChoice{Metadata: DailyCheckinRewardMetadata{
		BaseRewardAmount:    baseReward,
		RewardTier:          tier,
		StreakDays:          streakDays,
		StreakMultiplier:    streakMultiplier,
		CritEligible:        critEligible,
		CritHit:             critHit,
		CritMultiplier:      critMultiplier,
		PreCritRewardAmount: preCritReward,
		FinalRewardAmount:   finalReward,
	}}, nil
}

func hasDailyCheckinBudget(settings *DailyCheckinSettings) bool {
	return settings.DailyBudgetUSD > 0 || settings.MonthlyBudgetUSD > 0 || settings.UserMonthlyLimitUSD > 0
}

func maxDailyCheckinRewardByBudget(maxReward float64, dailyReward, monthlyReward, userMonthlyReward float64, settings *DailyCheckinSettings) float64 {
	if settings == nil {
		return maxReward
	}
	allowed := maxReward
	if settings.DailyBudgetUSD > 0 {
		allowed = math.Min(allowed, settings.DailyBudgetUSD-dailyReward)
	}
	if settings.MonthlyBudgetUSD > 0 {
		allowed = math.Min(allowed, settings.MonthlyBudgetUSD-monthlyReward)
	}
	if settings.UserMonthlyLimitUSD > 0 {
		allowed = math.Min(allowed, settings.UserMonthlyLimitUSD-userMonthlyReward)
	}
	return allowed
}

func canUseDailyCheckinBudgetFallback(rewardAmount, dailyReward float64, settings *DailyCheckinSettings) bool {
	if settings == nil || settings.DailyBudgetUSD <= 0 {
		return false
	}
	fallbackReward := normalizeDailyCheckinFallbackReward(rewardAmount)
	if fallbackReward <= 0 {
		return false
	}
	return dailyReward+settings.RewardMinUSD > settings.DailyBudgetUSD
}

func dailyCheckinBudgetExhaustedError(rewardAmount, dailyReward, monthlyReward, userMonthlyReward float64, settings *DailyCheckinSettings) error {
	dimension := "unknown"
	switch {
	case settings.DailyBudgetUSD > 0 && dailyReward+rewardAmount > settings.DailyBudgetUSD:
		dimension = "daily"
	case settings.MonthlyBudgetUSD > 0 && monthlyReward+rewardAmount > settings.MonthlyBudgetUSD:
		dimension = "monthly"
	case settings.UserMonthlyLimitUSD > 0 && userMonthlyReward+rewardAmount > settings.UserMonthlyLimitUSD:
		dimension = "user_monthly"
	}
	return ErrDailyCheckinBudgetExhausted.WithMetadata(map[string]string{
		"dimension": dimension,
	})
}

func isDailyCheckinBudgetExhausted(rewardAmount, dailyReward, monthlyReward, userMonthlyReward float64, settings *DailyCheckinSettings) bool {
	if settings == nil {
		return false
	}
	return (settings.DailyBudgetUSD > 0 && dailyReward+rewardAmount > settings.DailyBudgetUSD) ||
		(settings.MonthlyBudgetUSD > 0 && monthlyReward+rewardAmount > settings.MonthlyBudgetUSD) ||
		(settings.UserMonthlyLimitUSD > 0 && userMonthlyReward+rewardAmount > settings.UserMonthlyLimitUSD)
}

func lockDailyCheckinBudget(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT key FROM settings WHERE key = $1 FOR UPDATE`, SettingKeyDailyCheckinEnabled); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "syntax") || strings.Contains(msg, "near \"for\"") || strings.Contains(msg, "no such table") {
			return nil
		}
		return fmt.Errorf("lock daily checkin budget: %w", err)
	}
	return nil
}

func sumCheckinRewards(ctx context.Context, q dailyCheckinQuerier, userID int64, startDate, endDate string) (float64, error) {
	var total float64
	query := `
		SELECT COALESCE(SUM(reward_amount), 0)
		FROM user_checkins
		WHERE checkin_date >= $1 AND checkin_date < $2
	`
	args := []any{startDate, endDate}
	if userID > 0 {
		query += ` AND user_id = $3`
		args = append(args, userID)
	}
	if err := q.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum daily checkin rewards: %w", err)
	}
	return total, nil
}

func countDailyCheckinStreak(ctx context.Context, q dailyCheckinQuerier, userID int64, todayStart time.Time, settings *DailyCheckinSettings) (int, error) {
	startDate := ""
	if settings != nil && settings.StreakScope == DailyCheckinStreakScopeMonthly {
		startDate = timezone.StartOfMonth(todayStart).Format("2006-01-02")
	}
	query := `
		SELECT checkin_date
		FROM user_checkins
		WHERE user_id = $1 AND checkin_date < $2
	`
	args := []any{userID, todayStart.Format("2006-01-02")}
	if startDate != "" {
		query += ` AND checkin_date >= $3`
		args = append(args, startDate)
	}
	query += ` ORDER BY checkin_date DESC`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("count daily checkin streak: %w", err)
	}
	defer func() { _ = rows.Close() }()

	streak := 1
	expected := todayStart.AddDate(0, 0, -1).Format("2006-01-02")
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return 0, fmt.Errorf("scan daily checkin streak: %w", err)
		}
		if date != expected {
			break
		}
		streak++
		parsed, err := time.ParseInLocation("2006-01-02", expected, timezone.Location())
		if err != nil {
			break
		}
		expected = parsed.AddDate(0, 0, -1).Format("2006-01-02")
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate daily checkin streak: %w", err)
	}
	return streak, nil
}

func dailyCheckinStreakMultiplier(streakDays int, settings *DailyCheckinSettings) float64 {
	if settings == nil || !settings.StreakEnabled || streakDays <= 0 {
		return 1
	}
	multiplier := 1.0
	for _, item := range settings.StreakMultipliers {
		if streakDays >= item.Days {
			multiplier = item.Multiplier
		}
	}
	return normalizeDailyCheckinMultiplier(multiplier)
}

func aggregateCheckinStats(ctx context.Context, q dailyCheckinQuerier, startDate, endDate string) (int64, int64, float64, error) {
	var count, users int64
	var reward float64
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT user_id), COALESCE(SUM(reward_amount), 0)
		FROM user_checkins
		WHERE checkin_date >= $1 AND checkin_date < $2
	`, startDate, endDate).Scan(&count, &users, &reward); err != nil {
		return 0, 0, 0, fmt.Errorf("aggregate daily checkin stats: %w", err)
	}
	return count, users, reward, nil
}

func (s *DailyCheckinService) qualifiedUsersByDate(ctx context.Context, start, end time.Time, history []DailyCheckinRuleSnapshot) (map[string]map[int64]struct{}, error) {
	_, _, buckets := newOperationsOverviewPoints(start, end)
	requiredByDate := make(map[string]float64, len(buckets))
	qualified := make(map[string]map[int64]struct{}, len(buckets))
	for _, bucket := range buckets {
		rule := dailyCheckinRuleAt(history, bucket.End.Add(-time.Nanosecond))
		requiredByDate[bucket.Date] = rule.RequiredUsageUSD
		qualified[bucket.Date] = map[int64]struct{}{}
	}

	rawBuckets := buckets
	if operationsShouldUseDailyAggregates(buckets) {
		coveredFrom, coveredTo, ok, err := s.operationsDailyAggregateCoverage(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			aggregateBuckets, uncoveredBuckets := splitOperationsAggregateBuckets(buckets, coveredFrom, coveredTo, func(bucket operationsDateBucket) bool {
				return normalizeDailyCheckinUsageScope(dailyCheckinRuleAt(history, bucket.End.Add(-time.Nanosecond)).UsageScope) == DailyCheckinUsageScopeActualCost
			})
			if len(aggregateBuckets) > 0 {
				used, err := s.addQualifiedUsersFromDailyAggregates(ctx, aggregateBuckets, requiredByDate, qualified)
				if err != nil {
					return nil, err
				}
				if used {
					rawBuckets = uncoveredBuckets
				}
			}
		}
	}
	if err := s.addQualifiedUsersFromUsageLogs(ctx, rawBuckets, history, requiredByDate, qualified); err != nil {
		return nil, err
	}
	return qualified, nil
}

func (s *DailyCheckinService) addQualifiedUsersFromUsageLogs(ctx context.Context, buckets []operationsDateBucket, history []DailyCheckinRuleSnapshot, requiredByDate map[string]float64, qualified map[string]map[int64]struct{}) error {
	parts := make([]string, 0, len(buckets))
	args := make([]any, 0, len(buckets)*3)
	for _, bucket := range buckets {
		rule := dailyCheckinRuleAt(history, bucket.End.Add(-time.Nanosecond))
		dateArg := len(args) + 1
		startArg := dateArg + 1
		endArg := dateArg + 2
		extraWhere := ""
		if normalizeDailyCheckinUsageScope(rule.UsageScope) == DailyCheckinUsageScopeBalanceOnly {
			extraWhere = " AND subscription_id IS NULL"
		}
		parts = append(parts, fmt.Sprintf(`
			SELECT CAST($%d AS TEXT) AS bucket_date, user_id, COUNT(*), COALESCE(SUM(actual_cost), 0)
			FROM usage_logs
			WHERE created_at >= $%d AND created_at < $%d%s
			GROUP BY user_id
		`, dateArg, startArg, endArg, extraWhere))
		args = append(args, bucket.Date, bucket.Start, bucket.End)
	}
	query := strings.Join(parts, "\nUNION ALL\n")
	if query == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("aggregate qualified daily checkin users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var date string
		var userID int64
		var requestCount int64
		var actualCost float64
		if err := rows.Scan(&date, &userID, &requestCount, &actualCost); err != nil {
			return fmt.Errorf("scan qualified daily checkin users: %w", err)
		}
		if actualCost >= requiredByDate[date] {
			if qualified[date] == nil {
				qualified[date] = map[int64]struct{}{}
			}
			qualified[date][userID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate qualified daily checkin users: %w", err)
	}
	return nil
}

func (s *DailyCheckinService) addQualifiedUsersFromDailyAggregates(ctx context.Context, buckets []operationsDateBucket, requiredByDate map[string]float64, qualified map[string]map[int64]struct{}) (bool, error) {
	if len(buckets) == 0 {
		return false, nil
	}
	allowedDates := make(map[string]struct{}, len(buckets))
	for _, bucket := range buckets {
		allowedDates[bucket.Date] = struct{}{}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(bucket_date AS TEXT), user_id, COALESCE(actual_cost, 0)
		FROM usage_dashboard_daily_user_stats
		WHERE bucket_date >= $1 AND bucket_date < $2
		ORDER BY bucket_date ASC, user_id ASC
	`, buckets[0].Date, buckets[len(buckets)-1].End.Format("2006-01-02"))
	if err != nil {
		if isOperationsAggregateUnavailable(err) {
			return false, nil
		}
		return false, fmt.Errorf("aggregate qualified users from daily stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var date string
		var userID int64
		var actualCost float64
		if err := rows.Scan(&date, &userID, &actualCost); err != nil {
			return false, fmt.Errorf("scan qualified daily aggregate: %w", err)
		}
		if _, ok := allowedDates[date]; !ok {
			continue
		}
		if actualCost >= requiredByDate[date] {
			qualified[date][userID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate qualified daily aggregates: %w", err)
	}
	return true, nil
}

func (s *DailyCheckinService) dailyCheckinRuleHistory(ctx context.Context, current *DailyCheckinSettings) ([]DailyCheckinRuleSnapshot, error) {
	if s != nil && s.settingService != nil {
		return s.settingService.GetDailyCheckinRuleHistory(ctx)
	}
	rule := DailyCheckinRuleSnapshot{
		EffectiveAt:      time.Unix(0, 0).UTC(),
		RequiredUsageUSD: DailyCheckinRequiredUsageDefault,
		UsageScope:       DailyCheckinUsageScopeActualCost,
	}
	if current != nil {
		rule.RequiredUsageUSD = current.RequiredUsageUSD
		rule.UsageScope = normalizeDailyCheckinUsageScope(current.UsageScope)
	}
	return []DailyCheckinRuleSnapshot{rule}, nil
}

func dailyCheckinRuleAt(history []DailyCheckinRuleSnapshot, at time.Time) DailyCheckinRuleSnapshot {
	selected := DailyCheckinRuleSnapshot{
		EffectiveAt:      time.Unix(0, 0).UTC(),
		RequiredUsageUSD: DailyCheckinRequiredUsageDefault,
		UsageScope:       DailyCheckinUsageScopeActualCost,
	}
	for _, item := range history {
		if item.EffectiveAt.After(at) {
			break
		}
		selected = item
	}
	selected.RequiredUsageUSD = normalizeDailyCheckinNonNegativeFloat(selected.RequiredUsageUSD, DailyCheckinRequiredUsageDefault)
	selected.UsageScope = normalizeDailyCheckinUsageScope(selected.UsageScope)
	return selected
}

func (s *DailyCheckinService) listCheckinsForAnalytics(ctx context.Context, startDate, endDate string) ([]DailyCheckinAdminRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.user_id, COALESCE(u.username, ''), COALESCE(u.email, ''), c.checkin_date,
		       c.reward_amount, c.qualified_usage_usd, c.reward_metadata, c.created_at
		FROM user_checkins c
		JOIN users u ON u.id = c.user_id
		WHERE u.deleted_at IS NULL AND c.checkin_date >= $1 AND c.checkin_date < $2
		ORDER BY c.checkin_date ASC, c.id ASC
	`, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("list daily checkin analytics records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]DailyCheckinAdminRecord, 0)
	for rows.Next() {
		var item DailyCheckinAdminRecord
		if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.Email, &item.Date, &item.RewardAmount, &item.QualifiedUsageUSD, newRewardMetadataScanner(&item.RewardMetadata), &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan daily checkin analytics record: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily checkin analytics records: %w", err)
	}
	return items, nil
}

func rewardDistributionLabel(amount float64) string {
	switch {
	case amount < 1:
		return "<$1"
	case amount < 3:
		return "$1-$2.99"
	case amount < 5:
		return "$3-$4.99"
	default:
		return "$5+"
	}
}

func applyProjectedBudgetMetrics(summary *DailyCheckinAnalyticsSummary, points []DailyCheckinAnalyticsPoint, settings *DailyCheckinSettings) {
	if summary == nil || settings == nil {
		return
	}
	if settings.DailyBudgetUSD > 0 {
		rate := math.Max(0, math.Min(1, summary.DailyRemainingUSD/settings.DailyBudgetUSD))
		summary.DailyRemainingRate = &rate
	}
	start := len(points) - 7
	if start < 0 {
		start = 0
	}
	var rewardTotal float64
	var checkins int64
	var days int
	for _, point := range points[start:] {
		rewardTotal += point.RewardUSD
		checkins += point.CheckinUsers
		days++
	}
	if checkins > 0 && settings.DailyBudgetUSD > 0 {
		avgReward := rewardTotal / float64(checkins)
		if avgReward > 0 {
			value := math.Floor(summary.DailyRemainingUSD/avgReward*10) / 10
			summary.EstimatedCheckins = &value
		}
	}
	if settings.MonthlyBudgetUSD <= 0 || days == 0 || rewardTotal <= 0 {
		return
	}
	avgDaily := rewardTotal / float64(days)
	value := 0.0
	if summary.MonthlyRemainingUSD > 0 {
		value = math.Round((summary.MonthlyRemainingUSD/avgDaily)*10) / 10
	}
	summary.ProjectedBudgetDays = &value
}

func startOfDayInLocation(t time.Time) time.Time {
	loc := t.Location()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func operationsShouldUseDailyAggregates(buckets []operationsDateBucket) bool {
	return len(buckets) > 90 && len(buckets) > 0 && buckets[0].Start.Location().String() == timezone.Name()
}

func splitOperationsAggregateBuckets(buckets []operationsDateBucket, coveredFrom, coveredTo time.Time, predicate func(operationsDateBucket) bool) ([]operationsDateBucket, []operationsDateBucket) {
	aggregateBuckets := make([]operationsDateBucket, 0, len(buckets))
	rawBuckets := make([]operationsDateBucket, 0, 2)
	for _, bucket := range buckets {
		fullyCovered := !bucket.Start.Before(coveredFrom) && !bucket.End.After(coveredTo)
		if fullyCovered && (predicate == nil || predicate(bucket)) {
			aggregateBuckets = append(aggregateBuckets, bucket)
			continue
		}
		// Old data before aggregate coverage is intentionally left empty. Querying
		// raw logs there would recreate the long-range scan that aggregates avoid.
		if bucket.End.After(coveredTo) || (fullyCovered && predicate != nil) {
			rawBuckets = append(rawBuckets, bucket)
		}
	}
	return aggregateBuckets, rawBuckets
}

func (s *DailyCheckinService) operationsDailyAggregateCoverage(ctx context.Context) (time.Time, time.Time, bool, error) {
	var coveredFrom, coveredTo time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT user_daily_aggregated_from, user_daily_last_aggregated_at
		FROM usage_dashboard_aggregation_watermark
		WHERE id = 1
	`).Scan(&coveredFrom, &coveredTo)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isOperationsAggregateUnavailable(err) {
			return time.Time{}, time.Time{}, false, nil
		}
		return time.Time{}, time.Time{}, false, fmt.Errorf("read operations daily aggregate coverage: %w", err)
	}
	if !coveredTo.After(coveredFrom) || !coveredTo.After(time.Unix(0, 0)) {
		return time.Time{}, time.Time{}, false, nil
	}
	return coveredFrom, coveredTo, true, nil
}

func isOperationsAggregateUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "does not exist") ||
		strings.Contains(message, "undefined table") ||
		strings.Contains(message, "undefined column") ||
		strings.Contains(message, "no such table") ||
		strings.Contains(message, "no such column")
}

func (s *DailyCheckinService) operationsDataMeta(ctx context.Context, start, end time.Time) OperationsDataMeta {
	meta := OperationsDataMeta{
		Timezone:       start.Location().String(),
		AsOf:           time.Now().UTC().Format(time.RFC3339Nano),
		RequestedStart: start.Format("2006-01-02"),
		RequestedEnd:   end.AddDate(0, 0, -1).Format("2006-01-02"),
		DataQuality:    "complete",
		Source:         "usage_logs",
	}
	if s == nil || s.db == nil {
		meta.DataQuality = "unknown"
		meta.Warnings = []string{"usage_coverage_unavailable"}
		return meta
	}
	var minCreatedAtValue, maxCreatedAtValue any
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(created_at), MAX(created_at) FROM usage_logs`).Scan(&minCreatedAtValue, &maxCreatedAtValue); err != nil {
		meta.DataQuality = "unknown"
		meta.Warnings = []string{"usage_coverage_query_failed"}
		return meta
	}
	coverageStart := time.Time{}
	coverageEnd := time.Time{}
	if value, ok := operationsDatabaseTime(minCreatedAtValue); ok {
		coverageStart = value
	}
	if value, ok := operationsDatabaseTime(maxCreatedAtValue); ok {
		coverageEnd = value
	}
	if operationsShouldUseDailyAggregatesForRange(start, end) {
		aggregatedFrom, aggregatedTo, ok, err := s.operationsDailyAggregateCoverage(ctx)
		if err != nil {
			meta.Warnings = append(meta.Warnings, "daily_aggregate_coverage_query_failed")
		} else if ok {
			meta.Source = "usage_dashboard_daily_user_stats+usage_logs"
			if coverageStart.IsZero() || aggregatedFrom.Before(coverageStart) {
				coverageStart = aggregatedFrom
			}
			if aggregatedTo.After(coverageEnd) {
				coverageEnd = aggregatedTo
			}
		}
	}
	if coverageStart.IsZero() || coverageEnd.IsZero() {
		meta.DataQuality = "empty"
		return meta
	}
	coverageStart = coverageStart.In(start.Location())
	coverageEnd = coverageEnd.In(start.Location())
	meta.CoverageStart = coverageStart.Format("2006-01-02")
	meta.CoverageEnd = coverageEnd.Format("2006-01-02")
	if coverageStart.After(start) {
		meta.DataQuality = "partial"
		meta.Warnings = append(meta.Warnings, "requested_range_precedes_usage_coverage")
	}
	return meta
}

func operationsShouldUseDailyAggregatesForRange(start, end time.Time) bool {
	_, _, buckets := newOperationsOverviewPoints(start, end)
	return operationsShouldUseDailyAggregates(buckets)
}

func operationsDatabaseTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		return parseOperationsDatabaseTime(typed)
	case []byte:
		return parseOperationsDatabaseTime(string(typed))
	default:
		return time.Time{}, false
	}
}

func parseOperationsDatabaseTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func sortRewardDistribution(items []DailyCheckinRewardDistributionItem) {
	order := map[string]int{"<$1": 0, "$1-$2.99": 1, "$3-$4.99": 2, "$5+": 3}
	sort.Slice(items, func(i, j int) bool {
		left, okLeft := order[items[i].Label]
		right, okRight := order[items[j].Label]
		if okLeft && okRight {
			return left < right
		}
		return items[i].Label < items[j].Label
	})
}

func remainingBudget(limit, used float64) float64 {
	if limit <= 0 {
		return 0
	}
	remaining := limit - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

func buildDailyCheckinRecordWhere(filter DailyCheckinAdminRecordFilter) (string, []any) {
	clauses := []string{"u.deleted_at IS NULL"}
	args := make([]any, 0)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if strings.TrimSpace(filter.DateFrom) != "" {
		add("c.checkin_date >= $%d", strings.TrimSpace(filter.DateFrom))
	}
	if strings.TrimSpace(filter.DateTo) != "" {
		add("c.checkin_date <= $%d", strings.TrimSpace(filter.DateTo))
	}
	if strings.TrimSpace(filter.UserQuery) != "" {
		q := "%" + strings.ToLower(strings.TrimSpace(filter.UserQuery)) + "%"
		exact := strings.TrimSpace(filter.UserQuery)
		args = append(args, q, q, exact)
		clauses = append(clauses, fmt.Sprintf("(LOWER(u.email) LIKE $%d OR LOWER(u.username) LIKE $%d OR CAST(u.id AS TEXT) = $%d)", len(args)-2, len(args)-1, len(args)))
	}
	if filter.RewardMin != nil {
		add("c.reward_amount >= $%d", *filter.RewardMin)
	}
	if filter.RewardMax != nil {
		add("c.reward_amount <= $%d", *filter.RewardMax)
	}
	if filter.CritHit != nil {
		add("COALESCE(c.reward_metadata ->> 'crit_hit', 'false') = $%d", strconv.FormatBool(*filter.CritHit))
	}
	if filter.StreakDays != nil {
		add("COALESCE((c.reward_metadata ->> 'streak_days')::int, 0) >= $%d", *filter.StreakDays)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func dailyCheckinUsageNotEnoughError(todayUsage, requiredUsage float64) error {
	return ErrDailyCheckinUsageNotEnough.WithMetadata(map[string]string{
		"today_usage_usd":    fmt.Sprintf("%.4f", todayUsage),
		"required_usage_usd": fmt.Sprintf("%.2f", requiredUsage),
	})
}

func randomCentAmountInclusive(minValue, maxValue float64) (float64, error) {
	minValue, maxValue = normalizeDailyCheckinRewardRange(minValue, maxValue)
	minCents := int64(math.Round(minValue * 100))
	maxCents := int64(math.Round(maxValue * 100))
	if minCents == maxCents {
		return float64(minCents) / 100, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(maxCents-minCents+1))
	if err != nil {
		return 0, err
	}
	return float64(n.Int64()+minCents) / 100, nil
}

func randomDailyCheckinRewardFromTiers(settings *DailyCheckinSettings) (float64, *DailyCheckinRewardTier, error) {
	tiers := settings.RewardTiers
	if len(tiers) == 0 {
		tiers = []DailyCheckinRewardTier{{MinUSD: settings.RewardMinUSD, MaxUSD: settings.RewardMaxUSD, ProbabilityPercent: 100}}
	}
	totalWeight := 0
	weights := make([]int64, 0, len(tiers))
	for _, tier := range tiers {
		weight := int64(math.Round(normalizeDailyCheckinPercent(tier.ProbabilityPercent) * 10000))
		if weight < 0 {
			weight = 0
		}
		weights = append(weights, weight)
		totalWeight += int(weight)
	}
	if totalWeight <= 0 {
		return 0, nil, infraerrors.InternalServer("DAILY_CHECKIN_REWARD_TIERS_INVALID", "daily check-in reward tiers are invalid")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(totalWeight)))
	if err != nil {
		return 0, nil, infraerrors.InternalServer("DAILY_CHECKIN_REWARD_RANDOM_FAILED", "failed to generate daily check-in reward").WithCause(err)
	}
	pick := n.Int64()
	acc := int64(0)
	selected := tiers[len(tiers)-1]
	for i, tier := range tiers {
		acc += weights[i]
		if pick < acc {
			selected = tier
			break
		}
	}
	minValue, maxValue := normalizeDailyCheckinRewardRange(selected.MinUSD, selected.MaxUSD)
	reward, err := randomDailyCheckinReward(minValue, maxValue)
	if err != nil {
		return 0, nil, err
	}
	selected.MinUSD = minValue
	selected.MaxUSD = maxValue
	return reward, &selected, nil
}

func randomDailyCheckinPercentHit(probability float64) (bool, error) {
	probability = normalizeDailyCheckinPercent(probability)
	if probability <= 0 {
		return false, nil
	}
	if probability >= 100 {
		return true, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return false, infraerrors.InternalServer("DAILY_CHECKIN_CRIT_RANDOM_FAILED", "failed to generate daily check-in critical reward").WithCause(err)
	}
	return float64(n.Int64()) < probability*10000, nil
}

func roundDailyCheckinAmount(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return math.Round(value*100) / 100
}

func randomDailyCheckinReward(minValue, maxValue float64) (float64, error) {
	reward, err := randomCentAmountInclusive(minValue, maxValue)
	if err != nil {
		return 0, infraerrors.InternalServer("DAILY_CHECKIN_REWARD_RANDOM_FAILED", "failed to generate daily check-in reward").WithCause(err)
	}
	return reward, nil
}

func isDailyCheckinDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "idx_user_checkins_user_date") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}

func (s *DailyCheckinService) invalidateUserCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService == nil {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
	}()
}
