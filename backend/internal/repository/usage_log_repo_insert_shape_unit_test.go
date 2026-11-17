//go:build unit

package repository

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var (
	usageLogStaticInsertShapeRe = regexp.MustCompile(`(?s)INSERT INTO usage_logs \((.*?)\) VALUES \((.*?)\)`)
	usageLogPlaceholderRe       = regexp.MustCompile(`\$(\d+)`)
)

// newSQLCapturingMock 返回把实际下发 SQL 记录到 captured 的 sqlmock；语句一律视为匹配，
// 参数仍由 WithArgs 校验。
func newSQLCapturingMock(t *testing.T, captured *[]string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	matcher := sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
		*captured = append(*captured, actualSQL)
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// requireStaticInsertMatchesArgTypes 断言手写的 INSERT：列清单长度与 VALUES 占位符数量
// 都等于 usageLogInsertArgTypes，且占位符恰为 $1..$N 各出现一次。
func requireStaticInsertMatchesArgTypes(t *testing.T, query string) {
	t.Helper()
	m := usageLogStaticInsertShapeRe.FindStringSubmatch(query)
	require.Len(t, m, 3, "unrecognised INSERT shape:\n%s", query)

	want := len(usageLogInsertArgTypes)
	columns := 0
	for _, col := range strings.Split(m[1], ",") {
		if strings.TrimSpace(col) != "" {
			columns++
		}
	}
	require.Equal(t, want, columns, "INSERT column list must match usageLogInsertArgTypes")

	seen := make(map[int]struct{}, want)
	for _, ph := range usageLogPlaceholderRe.FindAllStringSubmatch(m[2], -1) {
		n, err := strconv.Atoi(ph[1])
		require.NoError(t, err)
		_, dup := seen[n]
		require.False(t, dup, "duplicate placeholder $%d", n)
		seen[n] = struct{}{}
	}
	require.Len(t, seen, want, "VALUES placeholder count must match usageLogInsertArgTypes")
	for i := 1; i <= want; i++ {
		_, ok := seen[i]
		require.True(t, ok, "missing placeholder $%d", i)
	}
}

// TestUsageLogStaticInsertShape_PlaceholdersMatchArgTypes 覆盖两条不经占位符生成器、
// 直接手写 $1..$N 的 INSERT 路径，防止加列后漏补占位符只在集成测试才暴露。
func TestUsageLogStaticInsertShape_PlaceholdersMatchArgTypes(t *testing.T) {
	upstreamRequestID := "20260902080329-oneapi"
	log := &service.UsageLog{
		UserID:            1,
		APIKeyID:          2,
		AccountID:         3,
		RequestID:         "client:insert-shape",
		UpstreamRequestID: &upstreamRequestID,
		Model:             "claude-3",
		InputTokens:       10,
		CreatedAt:         time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
	prepared := prepareUsageLogInsert(log)
	args := anySliceToDriverValues(prepared.args)

	t.Run("createSingle", func(t *testing.T) {
		var captured []string
		db, mock := newSQLCapturingMock(t, &captured)
		repo := &usageLogRepository{sql: db}

		mock.ExpectQuery("INSERT INTO usage_logs").
			WithArgs(args...).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), log.CreatedAt))

		inserted, err := repo.Create(context.Background(), log)
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Len(t, captured, 1)
		requireStaticInsertMatchesArgTypes(t, captured[0])
	})

	t.Run("execUsageLogInsertNoResult", func(t *testing.T) {
		var captured []string
		db, mock := newSQLCapturingMock(t, &captured)

		mock.ExpectExec("INSERT INTO usage_logs").
			WithArgs(args...).
			WillReturnResult(sqlmock.NewResult(0, 1))

		require.NoError(t, execUsageLogInsertNoResult(context.Background(), db, prepared))
		require.NoError(t, mock.ExpectationsWereMet())
		require.Len(t, captured, 1)
		requireStaticInsertMatchesArgTypes(t, captured[0])
	})
}

var (
	usageLogCTEShapeRe = regexp.MustCompile(
		`(?s)WITH input \((.*?)\) AS \(VALUES (.*?)\n.*?INSERT INTO usage_logs \((.*?)\)\s*SELECT(.*?)FROM input`)
	usageLogPlaceholderCastRe = regexp.MustCompile(`^\$(\d+)(?:::([a-z]+))?$`)
)

// splitSQLColumnList 把逗号分隔的列清单拆成去空白的列名切片。
func splitSQLColumnList(raw string) []string {
	parts := strings.Split(raw, ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		if c := strings.TrimSpace(p); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// captureStaticUsageLogInsertColumns 取 execUsageLogInsertNoResult 实际下发的 INSERT 列清单，
// 作为所有批量写入路径列顺序的基准。
func captureStaticUsageLogInsertColumns(t *testing.T, prepared usageLogInsertPrepared) []string {
	t.Helper()
	var captured []string
	db, mock := newSQLCapturingMock(t, &captured)
	mock.ExpectExec("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(prepared.args)...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, execUsageLogInsertNoResult(context.Background(), db, prepared))
	require.NoError(t, mock.ExpectationsWereMet())
	require.Len(t, captured, 1)

	m := usageLogStaticInsertShapeRe.FindStringSubmatch(captured[0])
	require.Len(t, m, 3, "unrecognised INSERT shape:\n%s", captured[0])
	cols := splitSQLColumnList(m[1])
	require.Len(t, cols, len(usageLogInsertArgTypes))
	return cols
}

// requireUsageLogCTEInsertShape 断言 `WITH input (...) AS (VALUES ...) INSERT ... SELECT ... FROM input`
// 形态的批量 SQL：CTE 列清单（去掉 leadingCTECols 前缀）、INSERT 列清单、SELECT 列清单三者
// 与基准列完全一致；每个 VALUES 行的占位符数等于 CTE 列数、编号连续，且类型转换与
// usageLogInsertArgTypes 同位。
func requireUsageLogCTEInsertShape(t *testing.T, query string, wantCols []string, leadingCTECols []string, rows int) {
	t.Helper()
	m := usageLogCTEShapeRe.FindStringSubmatch(query)
	require.Len(t, m, 5, "unrecognised CTE INSERT shape:\n%s", query)

	cteCols := splitSQLColumnList(m[1])
	require.Equal(t, leadingCTECols, cteCols[:len(leadingCTECols)], "CTE leading columns")
	require.Equal(t, wantCols, cteCols[len(leadingCTECols):], "CTE column list must match INSERT column list")
	require.Equal(t, wantCols, splitSQLColumnList(m[3]), "INSERT column list must match base INSERT")
	require.Equal(t, wantCols, splitSQLColumnList(m[4]), "SELECT column list must match INSERT column list")

	valuesRaw := strings.TrimSpace(m[2])
	require.True(t, strings.HasPrefix(valuesRaw, "(") && strings.HasSuffix(valuesRaw, ")"), "VALUES shape: %s", valuesRaw)
	tuples := strings.Split(valuesRaw[1:len(valuesRaw)-1], "),(")
	require.Len(t, tuples, rows, "VALUES row count")

	nextPlaceholder := 1
	for rowIdx, tuple := range tuples {
		items := splitSQLColumnList(tuple)
		require.Len(t, items, len(cteCols), "row %d placeholder count must match CTE column count", rowIdx)
		for i, item := range items {
			pm := usageLogPlaceholderCastRe.FindStringSubmatch(item)
			require.NotNil(t, pm, "row %d item %d is not a placeholder: %q", rowIdx, i, item)
			n, err := strconv.Atoi(pm[1])
			require.NoError(t, err)
			require.Equal(t, nextPlaceholder, n, "row %d item %d placeholder out of order", rowIdx, i)
			nextPlaceholder++
			if i < len(leadingCTECols) {
				continue
			}
			argIdx := i - len(leadingCTECols)
			require.Equal(t, usageLogInsertArgTypes[argIdx], pm[2],
				"row %d column %s cast must match usageLogInsertArgTypes", rowIdx, cteCols[i])
		}
	}
}

// TestUsageLogBatchInsertQueries_CTEColumnsMatchInsertColumns 防止批量写入的 CTE 列清单、
// INSERT 列清单、SELECT 列清单与 VALUES 占位符之间再次出现漏列/错位（例如 CTE 漏掉
// upstream_request_id 导致每个批次都在 Postgres 报错后退回逐条写入）。
func TestUsageLogBatchInsertQueries_CTEColumnsMatchInsertColumns(t *testing.T) {
	upstreamRequestID := "upstream-req-shape"
	sessionID := "sess-shape"
	newLog := func(requestID string) *service.UsageLog {
		return &service.UsageLog{
			UserID:            1,
			APIKeyID:          2,
			AccountID:         3,
			RequestID:         requestID,
			UpstreamRequestID: &upstreamRequestID,
			SessionID:         &sessionID,
			Model:             "gpt-5",
			CreatedAt:         time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		}
	}
	first := prepareUsageLogInsert(newLog("client:cte-shape-1"))
	second := prepareUsageLogInsert(newLog("client:cte-shape-2"))
	baseCols := captureStaticUsageLogInsertColumns(t, first)
	require.Contains(t, baseCols, "upstream_request_id")

	t.Run("bestEffort", func(t *testing.T) {
		query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{first, second})
		requireUsageLogCTEInsertShape(t, query, baseCols, []string{}, 2)
		require.Len(t, args, 2*len(usageLogInsertArgTypes))
	})

	t.Run("batch", func(t *testing.T) {
		k1 := usageLogBatchKey(first.requestID, 2)
		k2 := usageLogBatchKey(second.requestID, 2)
		query, args := buildUsageLogBatchInsertQuery([]string{k1, k2},
			map[string]usageLogInsertPrepared{k1: first, k2: second})
		requireUsageLogCTEInsertShape(t, query, baseCols, []string{"input_idx"}, 2)
		require.Len(t, args, 2*(len(usageLogInsertArgTypes)+1))
	})
}

// TestPrepareUsageLogInsert_UpstreamRequestIDArgWiring 把 upstream_request_id 钉在
// session_id 之前，与参数类型表保持同位；缺失时落 NULL 而不是空串。
func TestPrepareUsageLogInsert_UpstreamRequestIDArgWiring(t *testing.T) {
	upstreamRequestID := "req_upstream_123"
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:            1,
		APIKeyID:          2,
		RequestID:         "client:wiring",
		Model:             "gpt-5",
		UpstreamRequestID: &upstreamRequestID,
		CreatedAt:         time.Now().UTC(),
	})
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))

	idx := len(prepared.args) - 5
	arg, ok := prepared.args[idx].(sql.NullString)
	require.True(t, ok, "upstream_request_id arg should be sql.NullString, got %T", prepared.args[idx])
	require.True(t, arg.Valid)
	require.Equal(t, upstreamRequestID, arg.String)
	require.Equal(t, "text", usageLogInsertArgTypes[idx])

	absent := prepareUsageLogInsert(&service.UsageLog{UserID: 1, APIKeyID: 2, RequestID: "client:absent", Model: "gpt-5", CreatedAt: time.Now().UTC()})
	nullArg, ok := absent.args[idx].(sql.NullString)
	require.True(t, ok)
	require.False(t, nullArg.Valid, "absent upstream request id must be NULL")

	require.Contains(t, usageLogSelectColumns, "upstream_request_id")
}
