package service

import (
	"context"
	"database/sql"
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
			actual_cost REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL
		);
		CREATE TABLE user_checkins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			checkin_date TEXT NOT NULL,
			reward_amount REAL NOT NULL DEFAULT 0,
			qualified_usage_usd REAL NOT NULL DEFAULT 0,
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
	var usedBy int64
	require.NoError(t, db.QueryRow(`SELECT type, value, status, used_by, notes FROM redeem_codes WHERE used_by = 1`).Scan(&recordType, &value, &status, &usedBy, &notes))
	require.Equal(t, RedeemTypeCheckinBalance, recordType)
	require.Equal(t, 2.0, value)
	require.Equal(t, StatusUsed, status)
	require.Equal(t, int64(1), usedBy)
	require.Equal(t, "签到奖励", notes)

	_, err = svc.CheckIn(ctx, 1)
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_ALREADY_CHECKED_IN", apperrors.Reason(err))
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
	require.Equal(t, DailyCheckinRewardMaxLimit, result.RewardMinUSD)
	require.Equal(t, DailyCheckinRewardMaxLimit, result.RewardMaxUSD)
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
