package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const DailyCheckinRequiredUsageUSD = 1.0

var (
	ErrDailyCheckinAlreadyCheckedIn = infraerrors.Conflict("DAILY_CHECKIN_ALREADY_CHECKED_IN", "already checked in today")
	ErrDailyCheckinUsageNotEnough   = infraerrors.BadRequest("DAILY_CHECKIN_USAGE_NOT_ENOUGH", "daily usage is less than required")
)

type DailyCheckinRecord struct {
	ID                int64     `json:"id"`
	UserID            int64     `json:"user_id"`
	Date              string    `json:"date"`
	RewardAmount      float64   `json:"reward_amount"`
	QualifiedUsageUSD float64   `json:"qualified_usage_usd"`
	CreatedAt         time.Time `json:"created_at"`
}

type DailyCheckinStatus struct {
	Today            string               `json:"today"`
	Month            string               `json:"month"`
	CheckedIn        bool                 `json:"checked_in"`
	Eligible         bool                 `json:"eligible"`
	TodayUsageUSD    float64              `json:"today_usage_usd"`
	RequiredUsageUSD float64              `json:"required_usage_usd"`
	RewardMinUSD     int                  `json:"reward_min_usd"`
	RewardMaxUSD     int                  `json:"reward_max_usd"`
	MonthCheckins    []DailyCheckinRecord `json:"month_checkins"`
}

type DailyCheckinResult struct {
	DailyCheckinStatus
	RewardAmount float64   `json:"reward_amount"`
	Balance      float64   `json:"balance"`
	CheckedInAt  time.Time `json:"checked_in_at"`
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
	today := todayStart.Format("2006-01-02")

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

	todayUsage, err := sumTodayActualCost(ctx, tx, userID, todayStart, tomorrowStart)
	if err != nil {
		return nil, err
	}
	if todayUsage < DailyCheckinRequiredUsageUSD {
		return nil, dailyCheckinUsageNotEnoughError(todayUsage)
	}

	rewardMin, rewardMax, err := s.rewardRange(ctx)
	if err != nil {
		return nil, err
	}
	rewardInt, err := randomIntInclusive(rewardMin, rewardMax)
	if err != nil {
		return nil, infraerrors.InternalServer("DAILY_CHECKIN_REWARD_RANDOM_FAILED", "failed to generate daily check-in reward").WithCause(err)
	}
	rewardAmount := float64(rewardInt)

	var checkedInAt time.Time
	insertErr := tx.QueryRowContext(ctx, `
		INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at
	`, userID, today, rewardAmount, todayUsage, now).Scan(&checkedInAt)
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

	if err := insertDailyCheckinBalanceRecord(ctx, tx, userID, rewardAmount, now); err != nil {
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

func (s *DailyCheckinService) getStatus(ctx context.Context, q dailyCheckinQuerier, userID int64) (*DailyCheckinStatus, error) {
	now := timezone.Now()
	todayStart := timezone.StartOfDay(now)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	monthStart := timezone.StartOfMonth(now)
	nextMonthStart := monthStart.AddDate(0, 1, 0)

	today := todayStart.Format("2006-01-02")
	month := monthStart.Format("2006-01")

	todayUsage, err := sumTodayActualCost(ctx, q, userID, todayStart, tomorrowStart)
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
	rewardMin, rewardMax, err := s.rewardRange(ctx)
	if err != nil {
		return nil, err
	}

	return &DailyCheckinStatus{
		Today:            today,
		Month:            month,
		CheckedIn:        checkedIn,
		Eligible:         todayUsage >= DailyCheckinRequiredUsageUSD && !checkedIn,
		TodayUsageUSD:    todayUsage,
		RequiredUsageUSD: DailyCheckinRequiredUsageUSD,
		RewardMinUSD:     rewardMin,
		RewardMaxUSD:     rewardMax,
		MonthCheckins:    records,
	}, nil
}

func (s *DailyCheckinService) rewardRange(ctx context.Context) (int, int, error) {
	if s == nil || s.settingService == nil {
		return DailyCheckinRewardMinDefault, DailyCheckinRewardMaxDefault, nil
	}
	return s.settingService.GetDailyCheckinRewardRange(ctx)
}

func (s *DailyCheckinService) getCheckinByDate(ctx context.Context, q dailyCheckinQuerier, userID int64, date string) (*DailyCheckinRecord, error) {
	var record DailyCheckinRecord
	err := q.QueryRowContext(ctx, `
		SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, created_at
		FROM user_checkins
		WHERE user_id = $1 AND checkin_date = $2
	`, userID, date).Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get daily checkin: %w", err)
	}
	return &record, nil
}

func sumTodayActualCost(ctx context.Context, q dailyCheckinQuerier, userID int64, start, end time.Time) (float64, error) {
	var total float64
	if err := q.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(actual_cost), 0)
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`, userID, start, end).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum daily actual cost: %w", err)
	}
	return total, nil
}

func listMonthlyCheckins(ctx context.Context, q dailyCheckinQuerier, userID int64, monthStart, nextMonthStart string) ([]DailyCheckinRecord, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, user_id, checkin_date, reward_amount, qualified_usage_usd, created_at
		FROM user_checkins
		WHERE user_id = $1 AND checkin_date >= $2 AND checkin_date < $3
		ORDER BY checkin_date ASC
	`, userID, monthStart, nextMonthStart)
	if err != nil {
		return nil, fmt.Errorf("list monthly daily checkins: %w", err)
	}
	defer rows.Close()

	records := make([]DailyCheckinRecord, 0)
	for rows.Next() {
		var record DailyCheckinRecord
		if err := rows.Scan(&record.ID, &record.UserID, &record.Date, &record.RewardAmount, &record.QualifiedUsageUSD, &record.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan daily checkin: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily checkins: %w", err)
	}
	return records, nil
}

func insertDailyCheckinBalanceRecord(ctx context.Context, tx *sql.Tx, userID int64, rewardAmount float64, now time.Time) error {
	code, err := GenerateRedeemCode()
	if err != nil {
		return infraerrors.InternalServer("DAILY_CHECKIN_BALANCE_RECORD_CODE_FAILED", "failed to generate daily sign-in balance record code").WithCause(err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redeem_codes (code, type, value, status, used_by, used_at, notes, created_at, validity_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $6, 30)
	`, code, RedeemTypeCheckinBalance, rewardAmount, StatusUsed, userID, now, RedeemNotesDailyCheckinReward); err != nil {
		return fmt.Errorf("insert daily checkin balance record: %w", err)
	}
	return nil
}

func dailyCheckinUsageNotEnoughError(todayUsage float64) error {
	return ErrDailyCheckinUsageNotEnough.WithMetadata(map[string]string{
		"today_usage_usd":    fmt.Sprintf("%.4f", todayUsage),
		"required_usage_usd": fmt.Sprintf("%.2f", DailyCheckinRequiredUsageUSD),
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
