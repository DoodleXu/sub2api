package admin

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func newOperationsExportTestHandler(t *testing.T) (*SettingHandler, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			username TEXT,
			email TEXT,
			balance REAL NOT NULL DEFAULT 0,
			total_recharged REAL NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
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
			reward_metadata TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	require.NoError(t, err)
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	settingSvc := service.NewSettingService(repo, &config.Config{})
	checkinSvc := service.NewDailyCheckinService(db, settingSvc, nil, nil)
	return NewSettingHandler(settingSvc, nil, nil, nil, nil, nil, nil, checkinSvc), db
}

func TestOperationsExportRejectsInvalidDataset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := newOperationsExportTestHandler(t)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/export?dataset=bad", nil)

	handler.ExportOperationsData(c)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOperationsExportDailyCheckinRecordsCSV(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, db := newOperationsExportTestHandler(t)
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	_, err := db.Exec(`
		INSERT INTO users (id, username, email, balance, total_recharged, created_at, updated_at)
		VALUES (1, 'A, User', 'a@example.com', 0, 0, ?, ?)
	`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at)
		VALUES (1, '2026-06-14', 1.25, 2.50, ?)
	`, now)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/export?dataset=daily_checkin_records&start_date=2026-06-14&end_date=2026-06-14", nil)

	handler.ExportOperationsData(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/csv")
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, "\ufeff"))
	require.Contains(t, body, `"A, User"`)
	require.Contains(t, body, "a@example.com")
}

func TestOperationsExportDailyCheckinRecordsDefaultsToOperationsDateRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, db := newOperationsExportTestHandler(t)
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	_, err := db.Exec(`
		INSERT INTO users (id, username, email, balance, total_recharged, created_at, updated_at)
		VALUES
			(1, 'Recent User', 'recent@example.com', 0, 0, ?, ?),
			(2, 'Old User', 'old@example.com', 0, 0, ?, ?)
	`, now, now, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at)
		VALUES
			(1, ?, 1.00, 2.00, ?),
			(2, '2000-01-01', 2.00, 3.00, ?)
	`, today, now, now)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/export?dataset=daily_checkin_records&timezone=UTC", nil)

	handler.ExportOperationsData(c)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "recent@example.com")
	require.NotContains(t, body, "old@example.com")
	require.Contains(t, rec.Header().Get("Content-Disposition"), "operations_daily_checkin_records_")
}

func TestOperationsExportDailyCheckinRecordsUsesRecordDateFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, db := newOperationsExportTestHandler(t)
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	_, err := db.Exec(`
		INSERT INTO users (id, username, email, balance, total_recharged, created_at, updated_at)
		VALUES
			(1, 'Included User', 'included@example.com', 0, 0, ?, ?),
			(2, 'Excluded User', 'excluded@example.com', 0, 0, ?, ?)
	`, now, now, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO user_checkins (user_id, checkin_date, reward_amount, qualified_usage_usd, created_at)
		VALUES
			(1, '2026-06-14', 1.00, 2.00, ?),
			(2, '2026-06-01', 2.00, 3.00, ?)
	`, now, now)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/export?dataset=daily_checkin_records&start_date=2026-06-01&end_date=2026-06-30&date_from=2026-06-14&date_to=2026-06-14", nil)

	handler.ExportOperationsData(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Disposition"), "operations_daily_checkin_records_2026-06-14_to_2026-06-14.csv")
	body := rec.Body.String()
	require.Contains(t, body, "included@example.com")
	require.NotContains(t, body, "excluded@example.com")
}

func TestOperationsExportDailyCheckinRecordsQueryFailureBeforeCSVHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, db := newOperationsExportTestHandler(t)
	_, err := db.Exec(`DROP TABLE user_checkins`)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/export?dataset=daily_checkin_records&start_date=2026-06-14&end_date=2026-06-14", nil)

	handler.ExportOperationsData(c)

	require.NotEqual(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "\ufeff")
	require.NotContains(t, rec.Header().Get("Content-Type"), "text/csv")
}
