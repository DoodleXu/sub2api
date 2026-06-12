package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

type dailyCheckinSettingRepo struct {
	values map[string]string
}

func (r *dailyCheckinSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *dailyCheckinSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	values, err := r.GetMultiple(ctx, []string{key})
	if err != nil {
		return "", err
	}
	return values[key], nil
}

func (r *dailyCheckinSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *dailyCheckinSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = r.values[key]
	}
	return out, nil
}

func (r *dailyCheckinSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *dailyCheckinSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *dailyCheckinSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func newDailyCheckinTestService(t *testing.T, values map[string]string) (*DailyCheckinService, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			balance REAL NOT NULL DEFAULT 0,
			total_recharged REAL NOT NULL DEFAULT 0,
			updated_at TIMESTAMP,
			deleted_at TIMESTAMP
		);
		CREATE TABLE usage_logs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				subscription_id INTEGER,
				actual_cost REAL NOT NULL DEFAULT 0,
				created_at TIMESTAMP NOT NULL
			);
		CREATE TABLE user_checkins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
				checkin_date TEXT NOT NULL,
				reward_amount REAL NOT NULL DEFAULT 0,
				qualified_usage_usd REAL NOT NULL DEFAULT 0,
				redeem_code_id INTEGER,
				reward_metadata TEXT,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
		CREATE UNIQUE INDEX idx_user_checkins_user_date ON user_checkins (user_id, checkin_date);
		CREATE INDEX idx_user_checkins_user_month ON user_checkins (user_id, checkin_date);
		CREATE TABLE redeem_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			type TEXT NOT NULL,
			value REAL NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			used_by INTEGER,
			used_at TIMESTAMP,
			notes TEXT,
			created_at TIMESTAMP NOT NULL,
			validity_days INTEGER NOT NULL DEFAULT 30
		);
	`)
	require.NoError(t, err)

	repo := &dailyCheckinSettingRepo{values: values}
	settingService := NewSettingService(repo, &config.Config{})
	return NewDailyCheckinService(db, settingService, nil, nil), db
}

func TestDailyCheckinRequiresOneDollarUsage(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "2",
		SettingKeyDailyCheckinRewardMaxUSD: "2",
	})
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, timezone.Now())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 0.99, ?)`, timezone.Now())
	require.NoError(t, err)

	_, err = svc.CheckIn(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_USAGE_NOT_ENOUGH", apperrors.Reason(err))

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.False(t, status.Eligible)
	require.False(t, status.CheckedIn)
	require.InDelta(t, 0.99, status.TodayUsageUSD, 0.0001)
}

func TestDailyCheckinCanBeDisabled(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinEnabled: "false",
	})
	ctx := context.Background()
	now := timezone.Now()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2, ?)`, now)
	require.NoError(t, err)

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.False(t, status.Eligible)

	_, err = svc.CheckIn(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_DISABLED", apperrors.Reason(err))
}

func TestDailyCheckinRequiredUsageIsConfigurable(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRequiredUsageUSD: "2.5",
		SettingKeyDailyCheckinRewardMinUSD:     "1",
		SettingKeyDailyCheckinRewardMaxUSD:     "1",
	})
	ctx := context.Background()
	now := timezone.Now()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2.49, ?)`, now)
	require.NoError(t, err)

	_, err = svc.CheckIn(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_USAGE_NOT_ENOUGH", apperrors.Reason(err))

	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 0.01, ?)`, now)
	require.NoError(t, err)
	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, result.RewardAmount)
}

func TestDailyCheckinBalanceOnlyUsageScopeExcludesSubscriptionUsage(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinUsageScope:   DailyCheckinUsageScopeBalanceOnly,
		SettingKeyDailyCheckinRewardMinUSD: "1",
		SettingKeyDailyCheckinRewardMaxUSD: "1",
	})
	ctx := context.Background()
	now := timezone.Now()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, subscription_id, actual_cost, created_at) VALUES (1, 99, 3, ?)`, now)
	require.NoError(t, err)

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, DailyCheckinUsageScopeBalanceOnly, status.UsageScope)
	require.InDelta(t, 0, status.TodayUsageUSD, 0.0001)
	require.False(t, status.Eligible)

	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1, ?)`, now)
	require.NoError(t, err)
	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, result.RewardAmount)
}

func TestDailyCheckinStatusJSONDoesNotExposeOperationalFields(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD:        "1",
		SettingKeyDailyCheckinRewardMaxUSD:        "3",
		SettingKeyDailyCheckinDailyBudgetUSD:      "100",
		SettingKeyDailyCheckinMonthlyBudgetUSD:    "1000",
		SettingKeyDailyCheckinUserMonthlyLimitUSD: "10",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now).Format("2006-01-02")
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (id, user_id, checkin_date, reward_amount, qualified_usage_usd, redeem_code_id, created_at) VALUES (77, 1, ?, 2, 2, 88, ?)`, today, now)
	require.NoError(t, err)

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	body, err := json.Marshal(status)
	require.NoError(t, err)
	raw := string(body)
	require.NotContains(t, raw, "daily_budget_usd")
	require.NotContains(t, raw, "monthly_budget_usd")
	require.NotContains(t, raw, "daily_reward_usd")
	require.NotContains(t, raw, "monthly_reward_usd")
	require.NotContains(t, raw, "user_monthly_limit_usd")
	require.NotContains(t, raw, "user_monthly_reward_usd")
	require.NotContains(t, raw, "budget_exhausted")
	require.NotContains(t, raw, "usage_scope")
	require.NotContains(t, raw, "redeem_code_id")
	require.NotContains(t, raw, "user_id")
	require.NotContains(t, raw, `"id"`)
	require.Contains(t, raw, "month_checkins")
	require.Contains(t, raw, "reward_amount")
}

func TestDailyCheckinRewardsBalanceAndPreventsDuplicate(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "2",
		SettingKeyDailyCheckinRewardMaxUSD: "2",
	})
	ctx := context.Background()
	now := timezone.Now()
	yesterday := now.Add(-24 * time.Hour)

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 10, ?)`, yesterday)
	require.NoError(t, err)

	before, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.True(t, before.Eligible)
	require.False(t, before.CheckedIn)
	require.InDelta(t, 1.25, before.TodayUsageUSD, 0.0001)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 2.0, result.RewardAmount)
	require.Equal(t, 7.0, result.Balance)
	require.True(t, result.CheckedIn)
	require.False(t, result.Eligible)
	require.Len(t, result.MonthCheckins, 1)
	require.Equal(t, timezone.StartOfDay(now).Format("2006-01-02"), result.MonthCheckins[0].Date)

	var balance, totalRecharged float64
	require.NoError(t, db.QueryRow(`SELECT balance, total_recharged FROM users WHERE id = 1`).Scan(&balance, &totalRecharged))
	require.Equal(t, 7.0, balance)
	require.Equal(t, 20.0, totalRecharged)

	var recordType, status, notes string
	var value float64
	var usedBy, redeemCodeID, linkedRedeemCodeID int64
	require.NoError(t, db.QueryRow(`SELECT id, type, value, status, used_by, notes FROM redeem_codes WHERE used_by = 1`).Scan(&redeemCodeID, &recordType, &value, &status, &usedBy, &notes))
	require.Equal(t, RedeemTypeCheckinBalance, recordType)
	require.Equal(t, 2.0, value)
	require.Equal(t, StatusUsed, status)
	require.Equal(t, int64(1), usedBy)
	require.Equal(t, "签到奖励", notes)
	require.NoError(t, db.QueryRow(`SELECT redeem_code_id FROM user_checkins WHERE user_id = 1`).Scan(&linkedRedeemCodeID))
	require.Equal(t, redeemCodeID, linkedRedeemCodeID)

	_, err = svc.CheckIn(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_ALREADY_CHECKED_IN", apperrors.Reason(err))
}

func TestDailyCheckinSupportsDecimalRewardTierAmounts(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "1.25",
		SettingKeyDailyCheckinRewardMaxUSD: "1.25",
		SettingKeyDailyCheckinRewardTiers:  `[{"min_usd":1.25,"max_usd":1.25,"probability_percent":100}]`,
	})
	ctx := context.Background()
	now := timezone.Now()

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 1.25, status.RewardMinUSD, 0.0001)
	require.InDelta(t, 1.25, status.RewardMaxUSD, 0.0001)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.InDelta(t, 1.25, result.RewardAmount, 0.0001)
	require.InDelta(t, 1.25, result.BaseRewardAmount, 0.0001)
	require.InDelta(t, 6.25, result.Balance, 0.0001)

	var storedReward float64
	var metadataRaw string
	require.NoError(t, db.QueryRow(`SELECT reward_amount, reward_metadata FROM user_checkins WHERE user_id = 1`).Scan(&storedReward, &metadataRaw))
	require.InDelta(t, 1.25, storedReward, 0.0001)
	require.Contains(t, metadataRaw, `"min_usd":1.25`)
	require.Contains(t, metadataRaw, `"max_usd":1.25`)
}

func TestDailyCheckinAppliesRewardTierStreakAndCrit(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardTiers:       `[{"min_usd":2,"max_usd":2,"probability_percent":100}]`,
		SettingKeyDailyCheckinStreakEnabled:     "true",
		SettingKeyDailyCheckinStreakMultipliers: `[{"days":3,"multiplier":2}]`,
		SettingKeyDailyCheckinCritEnabled:       "true",
		SettingKeyDailyCheckinCritProbability:   "100",
		SettingKeyDailyCheckinCritMultiplier:    "3",
		SettingKeyDailyCheckinCritMaxRewardUSD:  "4",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now)
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2, ?)`, now)
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (1, ?, 1, 2, ?)`, today.AddDate(0, 0, -i).Format("2006-01-02"), now)
		require.NoError(t, err)
	}

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 12.0, result.RewardAmount)
	require.Equal(t, 3, result.StreakDays)
	require.Equal(t, 2.0, result.StreakMultiplier)
	require.True(t, result.CritHit)
	require.Equal(t, 3.0, result.CritMultiplier)
}

func TestDailyCheckinRegularCheckinCanCritWhenRewardWithinCap(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardTiers:      `[{"min_usd":2,"max_usd":2,"probability_percent":100}]`,
		SettingKeyDailyCheckinStreakEnabled:    "false",
		SettingKeyDailyCheckinCritEnabled:      "true",
		SettingKeyDailyCheckinCritProbability:  "100",
		SettingKeyDailyCheckinCritMultiplier:   "3",
		SettingKeyDailyCheckinCritMaxRewardUSD: "2",
	})
	ctx := context.Background()
	now := timezone.Now()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2, ?)`, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 6.0, result.RewardAmount)
	require.Equal(t, 2.0, result.BaseRewardAmount)
	require.Equal(t, 2.0, result.PreCritRewardAmount)
	require.Equal(t, 1, result.StreakDays)
	require.Equal(t, 1.0, result.StreakMultiplier)
	require.True(t, result.CritHit)
	require.Equal(t, 3.0, result.CritMultiplier)
}

func TestDailyCheckinCritSkipsWhenPreCritRewardExceedsCap(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardTiers:       `[{"min_usd":2,"max_usd":2,"probability_percent":100}]`,
		SettingKeyDailyCheckinStreakEnabled:     "true",
		SettingKeyDailyCheckinStreakMultipliers: `[{"days":2,"multiplier":2}]`,
		SettingKeyDailyCheckinCritEnabled:       "true",
		SettingKeyDailyCheckinCritProbability:   "100",
		SettingKeyDailyCheckinCritMultiplier:    "3",
		SettingKeyDailyCheckinCritMaxRewardUSD:  "3",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now)
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (1, ?, 1, 2, ?)`, today.AddDate(0, 0, -1).Format("2006-01-02"), now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 4.0, result.RewardAmount)
	require.Equal(t, 2, result.StreakDays)
	require.False(t, result.CritHit)
}

func TestDailyCheckinZeroRewardConfigIsRaisedToMinimumReward(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "0",
		SettingKeyDailyCheckinRewardMaxUSD: "0",
		SettingKeyDailyCheckinRewardTiers:  `[{"min_usd":0,"max_usd":0,"probability_percent":100}]`,
	})
	ctx := context.Background()
	now := timezone.Now()
	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 2, ?)`, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, result.RewardAmount)
	require.InDelta(t, 1, result.RewardMinUSD, 0.0001)
	require.InDelta(t, 1, result.RewardMaxUSD, 0.0001)
}

func TestDailyCheckinRewardRangeIsClampedToMaxLimit(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "500",
		SettingKeyDailyCheckinRewardMaxUSD: "1000",
	})
	ctx := context.Background()
	now := timezone.Now()

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, float64(DailyCheckinRewardMaxLimit), result.RewardAmount)
	require.Equal(t, 105.0, result.Balance)
	require.InDelta(t, DailyCheckinRewardMaxLimit, result.RewardMinUSD, 0.0001)
	require.InDelta(t, DailyCheckinRewardMaxLimit, result.RewardMaxUSD, 0.0001)
}

func TestDailyCheckinDailyBudgetFallbackGrantsMinimumReward(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD:       "2",
		SettingKeyDailyCheckinRewardMaxUSD:       "2",
		SettingKeyDailyCheckinDailyBudgetUSD:     "3",
		SettingKeyDailyCheckinBudgetFallbackUSD:  "0.01",
		SettingKeyDailyCheckinBudgetFallbackText: "今日签到预算已用完哦～奖励0.01",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now).Format("2006-01-02")

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (2, ?, 2, 2, ?)`, today, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 0.01, result.RewardAmount)
	require.True(t, result.BudgetFallback)
	require.Equal(t, "今日签到预算已用完哦～奖励0.01", result.Message)
	require.Equal(t, 0.01, result.BaseRewardAmount)
	require.Equal(t, 5.01, result.Balance)
}

func TestDailyCheckinStatusReflectsGlobalBudgetExhaustion(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD:   "2",
		SettingKeyDailyCheckinRewardMaxUSD:   "2",
		SettingKeyDailyCheckinDailyBudgetUSD: "3",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now).Format("2006-01-02")

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (2, ?, 2, 2, ?)`, today, now)
	require.NoError(t, err)

	status, err := svc.GetStatus(ctx, 1)
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.True(t, status.Eligible)
	require.True(t, status.BudgetExhausted)
	require.Equal(t, 2.0, status.DailyRewardUSD)
}

func TestDailyCheckinBudgetFallbackIgnoresOtherBudgetLimits(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD:        "2",
		SettingKeyDailyCheckinRewardMaxUSD:        "2",
		SettingKeyDailyCheckinDailyBudgetUSD:      "3",
		SettingKeyDailyCheckinMonthlyBudgetUSD:    "2",
		SettingKeyDailyCheckinUserMonthlyLimitUSD: "2",
		SettingKeyDailyCheckinBudgetFallbackUSD:   "0.01",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now).Format("2006-01-02")

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (2, ?, 2, 2, ?)`, today, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 0.01, result.RewardAmount)
	require.True(t, result.BudgetFallback)
}

func TestDailyCheckinRewardClampsToRemainingBudget(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD:   "1",
		SettingKeyDailyCheckinRewardMaxUSD:   "3",
		SettingKeyDailyCheckinDailyBudgetUSD: "3",
	})
	ctx := context.Background()
	now := timezone.Now()
	today := timezone.StartOfDay(now).Format("2006-01-02")

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at) VALUES (2, ?, 1, 2, ?)`, today, now)
	require.NoError(t, err)

	result, err := svc.CheckIn(ctx, 1)
	require.NoError(t, err)
	require.GreaterOrEqual(t, result.RewardAmount, 1.0)
	require.LessOrEqual(t, result.RewardAmount, 2.0)
}

func TestDailyCheckinConcurrentRequestsDoNotDoubleReward(t *testing.T) {
	svc, db := newDailyCheckinTestService(t, map[string]string{
		SettingKeyDailyCheckinRewardMinUSD: "2",
		SettingKeyDailyCheckinRewardMaxUSD: "2",
	})
	ctx := context.Background()
	now := timezone.Now()

	_, err := db.Exec(`INSERT INTO users (id, balance, total_recharged, updated_at) VALUES (1, 5, 20, ?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO usage_logs (user_id, actual_cost, created_at) VALUES (1, 1.25, ?)`, now)
	require.NoError(t, err)

	const workers = 8
	var wg sync.WaitGroup
	var successes int32
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.CheckIn(ctx, 1); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), successes)

	var checkinCount, balanceRecordCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_checkins WHERE user_id = 1`).Scan(&checkinCount))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by = 1 AND type = ?`, RedeemTypeCheckinBalance).Scan(&balanceRecordCount))
	require.Equal(t, 1, checkinCount)
	require.Equal(t, 1, balanceRecordCount)

	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id = 1`).Scan(&balance))
	require.Equal(t, 7.0, balance)
}
