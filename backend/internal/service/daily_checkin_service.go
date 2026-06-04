package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
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
	ID                int64     `json:"-"`
	UserID            int64     `json:"-"`
	Date              string    `json:"date"`
	RewardAmount      float64   `json:"reward_amount"`
	QualifiedUsageUSD float64   `json:"qualified_usage_usd"`
	RedeemCodeID      *int64    `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
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
	RewardMinUSD         int                  `json:"reward_min_usd"`
	RewardMaxUSD         int                  `json:"reward_max_usd"`
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
	RewardAmount float64   `json:"reward_amount"`
	Balance      float64   `json:"balance"`
	CheckedInAt  time.Time `json:"checked_in_at"`
}

type DailyCheckinAdminStats struct {
	Enabled             bool    `json:"enabled"`
	RequiredUsageUSD    float64 `json:"required_usage_usd"`
	UsageScope          string  `json:"usage_scope"`
	RewardMinUSD        int     `json:"reward_min_usd"`
	RewardMaxUSD        int     `json:"reward_max_usd"`
	TodayCheckins       int64   `json:"today_checkins"`
	TodayUsers          int64   `json:"today_users"`
	TodayRewardUSD      float64 `json:"today_reward_usd"`
	MonthCheckins       int64   `json:"month_checkins"`
	MonthUsers          int64   `json:"month_users"`
	MonthRewardUSD      float64 `json:"month_reward_usd"`
	AverageRewardUSD    float64 `json:"average_reward_usd"`
	DailyBudgetUSD      float64 `json:"daily_budget_usd"`
	DailyRemainingUSD   float64 `json:"daily_remaining_usd"`
	MonthlyBudgetUSD    float64 `json:"monthly_budget_usd"`
	MonthlyRemainingUSD float64 `json:"monthly_remaining_usd"`
	UserMonthlyLimitUSD float64 `json:"user_monthly_limit_usd"`
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

	rewardInt, err := chooseDailyCheckinReward(ctx, tx, userID, todayStart, tomorrowStart, monthStart, nextMonthStart, settings)
	if err != nil {
		return nil, err
	}
	rewardAmount := float64(rewardInt)

	var checkedInAt time.Time
	var checkinID int64
	insertErr := tx.QueryRowContext(ctx, `
			INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id, created_at
		`, userID, today, rewardAmount, todayUsage, now).Scan(&checkinID, &checkedInAt)
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
		DailyCheckinStatus: *status,
		RewardAmount:       rewardAmount,
		Balance:            balance,
		CheckedInAt:        checkedInAt,
	}, nil
}

func (s *DailyCheckinService) GetAdminStats(ctx context.Context) (*DailyCheckinAdminStats, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_UNAVAILABLE", "daily check-in service is unavailable")
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}
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
	}, nil
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
	budgetExhausted := isDailyCheckinBudgetExhausted(float64(settings.RewardMinUSD), dailyReward, monthlyReward, userMonthlyReward, settings)

	return &DailyCheckinStatus{
		Enabled:              settings.Enabled,
		Today:                today,
		Month:                month,
		CheckedIn:            checkedIn,
		Eligible:             settings.Enabled && todayUsage >= settings.RequiredUsageUSD && !checkedIn && !budgetExhausted,
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
			Enabled:          true,
			RequiredUsageUSD: DailyCheckinRequiredUsageDefault,
			UsageScope:       DailyCheckinUsageScopeActualCost,
			RewardMinUSD:     DailyCheckinRewardMinDefault,
			RewardMaxUSD:     DailyCheckinRewardMaxDefault,
		}, nil
	}
	return s.settingService.GetDailyCheckinSettings(ctx)
}

func (s *DailyCheckinService) getCheckinByDate(ctx context.Context, q dailyCheckinQuerier, userID int64, date string) (*DailyCheckinRecord, error) {
	var record DailyCheckinRecord
	var redeemCodeID sql.NullInt64
	err := q.QueryRowContext(ctx, `
				SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, redeem_code_id, created_at
				FROM user_checkins
				WHERE user_id = $1 AND checkin_date = $2
			`, userID, date).Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &redeemCodeID, &record.CreatedAt)
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
			SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, redeem_code_id, created_at
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
		if err := rows.Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &redeemCodeID, &record.CreatedAt); err != nil {
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

func chooseDailyCheckinReward(ctx context.Context, tx *sql.Tx, userID int64, todayStart, tomorrowStart, monthStart, nextMonthStart time.Time, settings *DailyCheckinSettings) (int, error) {
	if settings == nil {
		return 0, infraerrors.InternalServer("DAILY_CHECKIN_SETTINGS_MISSING", "daily check-in settings are missing")
	}
	if !hasDailyCheckinBudget(settings) {
		return randomDailyCheckinReward(settings.RewardMinUSD, settings.RewardMaxUSD)
	}
	if err := lockDailyCheckinBudget(ctx, tx); err != nil {
		return 0, err
	}
	dailyReward, err := sumCheckinRewards(ctx, tx, 0, todayStart.Format("2006-01-02"), tomorrowStart.Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	monthlyReward, err := sumCheckinRewards(ctx, tx, 0, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	userMonthlyReward, err := sumCheckinRewards(ctx, tx, userID, monthStart.Format("2006-01-02"), nextMonthStart.Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	maxReward := maxDailyCheckinRewardByBudget(settings.RewardMaxUSD, dailyReward, monthlyReward, userMonthlyReward, settings)
	if maxReward < settings.RewardMinUSD {
		return 0, dailyCheckinBudgetExhaustedError(float64(settings.RewardMinUSD), dailyReward, monthlyReward, userMonthlyReward, settings)
	}
	return randomDailyCheckinReward(settings.RewardMinUSD, maxReward)
}

func hasDailyCheckinBudget(settings *DailyCheckinSettings) bool {
	return settings.DailyBudgetUSD > 0 || settings.MonthlyBudgetUSD > 0 || settings.UserMonthlyLimitUSD > 0
}

func maxDailyCheckinRewardByBudget(maxReward int, dailyReward, monthlyReward, userMonthlyReward float64, settings *DailyCheckinSettings) int {
	if settings == nil {
		return maxReward
	}
	allowed := maxReward
	if settings.DailyBudgetUSD > 0 {
		allowed = min(allowed, int(math.Floor(settings.DailyBudgetUSD-dailyReward)))
	}
	if settings.MonthlyBudgetUSD > 0 {
		allowed = min(allowed, int(math.Floor(settings.MonthlyBudgetUSD-monthlyReward)))
	}
	if settings.UserMonthlyLimitUSD > 0 {
		allowed = min(allowed, int(math.Floor(settings.UserMonthlyLimitUSD-userMonthlyReward)))
	}
	return allowed
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

func dailyCheckinUsageNotEnoughError(todayUsage, requiredUsage float64) error {
	return ErrDailyCheckinUsageNotEnough.WithMetadata(map[string]string{
		"today_usage_usd":    fmt.Sprintf("%.4f", todayUsage),
		"required_usage_usd": fmt.Sprintf("%.2f", requiredUsage),
	})
}

func randomIntInclusive(minValue, maxValue int) (int, error) {
	minValue, maxValue = normalizeDailyCheckinRewardRange(minValue, maxValue)
	if minValue == maxValue {
		return minValue, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxValue-minValue+1)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()) + minValue, nil
}

func randomDailyCheckinReward(minValue, maxValue int) (int, error) {
	reward, err := randomIntInclusive(minValue, maxValue)
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
