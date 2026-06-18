package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildContentModerationLogWhere_BlockedIncludesAllBlockActions(t *testing.T) {
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{Result: "blocked"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.action IN ('block', 'keyword_block', 'hash_block')")
	require.NotContains(t, sql, "l.action = 'block'")
}

func TestContentModerationRepositoryCountFlaggedByUserSince_ExcludesHashBlock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("AND action <> 'hash_block'")).
		WithArgs(int64(1001), since, false).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	count, err := repo.CountFlaggedByUserSince(context.Background(), 1001, since, false)

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryCountFlaggedByUserSince_ExcludesCyberPolicyWhenRequested(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("AND ($3::bool IS FALSE OR action <> 'cyber_policy')")).
		WithArgs(int64(1001), since, true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	count, err := repo.CountFlaggedByUserSince(context.Background(), 1001, since, true)

	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryListLogs_ReturnsEmailDedupeStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	createdAt := time.Date(2026, 6, 5, 3, 43, 9, 0, time.UTC)
	lastEmailSentAt := time.Date(2026, 6, 5, 4, 15, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM content_moderation_logs l WHERE l.id IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	rows := sqlmock.NewRows([]string{
		"id", "request_id", "user_id", "user_email", "api_key_id", "api_key_name", "group_id", "group_name",
		"endpoint", "provider", "model", "mode", "action", "flagged", "highest_category", "highest_score",
		"category_scores", "threshold_snapshot", "input_excerpt", "input_hash", "matched_keyword",
		"policy_id", "policy_action", "policy_snapshot", "block_status", "error_code", "upstream_latency_ms", "error",
		"violation_count", "auto_banned", "email_sent", "email_deduped", "last_email_sent_at", "user_status", "queue_delay_ms", "created_at",
	}).AddRow(
		int64(1), "req-1", int64(1001), "user@example.com", nil, "", nil, "",
		"/v1/responses", "openai", "gpt-5", "pre_block", service.ContentModerationActionHashBlock, true, "sexual", 0.9,
		[]byte(`{"sexual":0.9}`), []byte(`{"sexual":0.7}`), "blocked prompt", strings.Repeat("a", 64), "",
		nil, "", []byte(`{}`), 403, "content_policy_violation", nil, "",
		1, false, false, true, lastEmailSentAt, "active", nil, createdAt,
	)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT")).
		WithArgs(20, 0).
		WillReturnRows(rows)

	items, page, err := repo.ListLogs(context.Background(), service.ContentModerationLogFilter{})

	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, items, 1)
	require.False(t, items[0].EmailSent)
	require.True(t, items[0].EmailDeduped)
	require.NotNil(t, items[0].LastEmailSentAt)
	require.Equal(t, lastEmailSentAt, *items[0].LastEmailSentAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
