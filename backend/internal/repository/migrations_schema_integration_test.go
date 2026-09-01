//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMigrationsRunner_ConcurrentInstancesSerializeOnSessionLock(t *testing.T) {
	const instances = 2
	errorsByInstance := make([]error, instances)
	var wg sync.WaitGroup
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errorsByInstance[index] = ApplyMigrations(ctx, integrationDB)
		}(i)
	}
	wg.Wait()
	for i, err := range errorsByInstance {
		require.NoErrorf(t, err, "migration instance %d", i)
	}
}

func TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate(t *testing.T) {
	tx := testTx(t)

	// Re-apply migrations to verify idempotency (no errors, no duplicate rows).
	require.NoError(t, ApplyMigrations(context.Background(), integrationDB))

	// schema_migrations should have at least the current migration set.
	var applied int
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&applied))
	require.GreaterOrEqual(t, applied, 7, "expected schema_migrations to contain applied migrations")

	// users: columns required by repository queries
	requireColumn(t, tx, "users", "username", "character varying", 100, false)
	requireColumn(t, tx, "users", "notes", "text", 0, false)

	// accounts: schedulable and rate-limit fields
	requireColumn(t, tx, "accounts", "notes", "text", 0, true)
	requireColumn(t, tx, "accounts", "schedulable", "boolean", 0, false)
	requireColumn(t, tx, "accounts", "rate_limited_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "rate_limit_reset_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "overload_until", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "accounts", "session_window_status", "character varying", 20, true)
	requireIndex(t, tx, "accounts", "idx_accounts_autopause_expiry_due")

	// groups: OpenAI Live 默认关闭，管理员显式开启后才可访问。
	requireColumn(t, tx, "groups", "allow_live", "boolean", 0, false)

	// api_keys: key length should be 128
	requireColumn(t, tx, "api_keys", "key", "character varying", 128, false)

	// redeem_codes: subscription fields
	requireColumn(t, tx, "redeem_codes", "group_id", "bigint", 0, true)
	requireColumn(t, tx, "redeem_codes", "validity_days", "integer", 0, false)

	// usage_logs: billing_type used by filters/stats
	requireColumn(t, tx, "usage_logs", "billing_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "request_type", "smallint", 0, false)
	requireColumn(t, tx, "usage_logs", "openai_ws_mode", "boolean", 0, false)
	requireColumn(t, tx, "usage_logs", "native_compaction_v2", "boolean", 0, false)
	requireColumnDefaultContains(t, tx, "usage_logs", "native_compaction_v2", "false")
	requireColumn(t, tx, "usage_logs", "image_input_size", "character varying", 32, true)
	requireColumn(t, tx, "usage_logs", "image_output_size", "character varying", 32, true)
	requireColumn(t, tx, "usage_logs", "image_size_source", "character varying", 16, true)
	requireColumn(t, tx, "usage_logs", "image_size_breakdown", "jsonb", 0, true)
	requireColumn(t, tx, "usage_logs", "video_count", "integer", 0, false)
	requireColumn(t, tx, "usage_logs", "video_resolution", "character varying", 10, true)
	requireColumn(t, tx, "usage_logs", "video_duration_seconds", "integer", 0, true)
	requireColumn(t, tx, "usage_logs", "image_first_output_ms", "integer", 0, true)
	requireColumn(t, tx, "usage_logs", "upstream_response_model", "character varying", 200, true)
	requireColumn(t, tx, "usage_logs", "upstream_model_mismatch", "boolean", 0, true)
	requireIndex(t, tx, "usage_logs", usageLogsUpstreamModelMismatchIndex)
	requireIndex(t, tx, "usage_logs", "idx_usage_logs_account_id_id")
	requireForeignKeyOnDelete(t, tx, "usage_logs", "account_id", "accounts", "CASCADE")

	requireColumn(t, tx, "usage_account_cost_totals", "last_processed_usage_id", "bigint", 0, false)
	requireColumn(t, tx, "usage_account_cost_totals", "initialized", "boolean", 0, false)
	requireColumn(t, tx, "usage_account_cost_totals", "needs_processing", "boolean", 0, false)
	requireColumn(t, tx, "usage_account_cost_totals", "published_account_cost", "numeric", 0, false)
	requireColumn(t, tx, "usage_account_cost_totals", "published_standard_account_cost", "numeric", 0, false)
	requireColumn(t, tx, "usage_account_cost_totals", "published_initialized", "boolean", 0, false)
	requireIndex(t, tx, "usage_account_cost_totals", "idx_usage_account_cost_totals_pending")
	requireForeignKeyOnDelete(t, tx, "usage_account_cost_totals", "account_id", "accounts", "CASCADE")
	requireIndex(t, tx, "usage_account_cost_dirty_buckets", "idx_usage_account_cost_dirty_buckets_requested")
	requireColumn(t, tx, "usage_dashboard_cost_snapshot", "ledger_pending", "boolean", 0, false)
	requireColumn(t, tx, "usage_dashboard_cost_snapshot", "data_through", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "usage_dashboard_cost_snapshot", "stale_reason", "text", 0, true)

	requireColumn(t, tx, "usage_dashboard_account_model_hourly", "account_id", "bigint", 0, false)
	requireColumn(t, tx, "usage_dashboard_account_model_hourly", "model", "text", 0, false)
	requireColumn(t, tx, "usage_dashboard_account_model_hourly", "standard_cost", "numeric", 0, false)
	requireIndex(t, tx, "usage_dashboard_account_model_hourly", "idx_usage_dashboard_account_model_hourly_bucket")
	requireColumn(t, tx, "usage_dashboard_account_model_daily", "account_id", "bigint", 0, false)
	requireColumn(t, tx, "usage_dashboard_account_model_daily", "model", "text", 0, false)
	requireColumn(t, tx, "usage_dashboard_account_model_daily", "standard_cost", "numeric", 0, false)
	requireIndex(t, tx, "usage_dashboard_account_model_daily", "idx_usage_dashboard_account_model_daily_bucket")

	var mismatchIndexDef string
	require.NoError(t, tx.QueryRowContext(context.Background(), `
SELECT pg_get_indexdef(i.indexrelid)
FROM pg_class idx
JOIN pg_index i ON i.indexrelid = idx.oid
JOIN pg_class tbl ON tbl.oid = i.indrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = 'usage_logs'
  AND idx.relname = $1
`, usageLogsUpstreamModelMismatchIndex).Scan(&mismatchIndexDef))
	require.Contains(t, mismatchIndexDef, "created_at DESC")
	require.Contains(t, mismatchIndexDef, "id DESC")
	require.Contains(t, mismatchIndexDef, "WHERE (upstream_model_mismatch IS TRUE)")
	requireColumn(t, tx, "ops_metrics_hourly", "image_generation_ttft_sample_count", "bigint", 0, false)
	requireColumn(t, tx, "ops_metrics_hourly", "image_generation_ttft_avg_ms", "double precision", 0, true)
	requireColumn(t, tx, "ops_metrics_daily", "image_generation_ttft_sample_count", "bigint", 0, false)
	requireColumn(t, tx, "ops_metrics_daily", "image_generation_ttft_avg_ms", "double precision", 0, true)
	requireConstraintDefinitionContains(
		t,
		tx,
		"usage_logs",
		"usage_logs_image_size_source_check",
		"image_size_source",
		"'output'",
		"'input'",
		"'default'",
		"'legacy'",
	)
	requireConstraintDefinitionContains(
		t,
		tx,
		"usage_logs",
		"usage_logs_image_billing_size_check",
		"image_count",
		"billing_mode",
		"'video'",
		"video_count",
		"image_size IS NOT NULL",
		"'1K'",
		"'2K'",
		"'4K'",
		"'mixed'",
	)

	// usage_billing_dedup: billing idempotency narrow table
	var usageBillingDedupRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup')").Scan(&usageBillingDedupRegclass))
	require.True(t, usageBillingDedupRegclass.Valid, "expected usage_billing_dedup table to exist")
	requireColumn(t, tx, "usage_billing_dedup", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_request_api_key")
	requireIndex(t, tx, "usage_billing_dedup", "idx_usage_billing_dedup_created_at_brin")

	// grok_video_tasks: durable ownership and billing snapshots for async video.
	var grokVideoTasksRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.grok_video_tasks')").Scan(&grokVideoTasksRegclass))
	require.True(t, grokVideoTasksRegclass.Valid, "expected grok_video_tasks table to exist")
	requireColumn(t, tx, "grok_video_tasks", "request_id", "character varying", 255, false)
	requireColumn(t, tx, "grok_video_tasks", "billing_claimed_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "grok_video_tasks", "billed_at", "timestamp with time zone", 0, true)
	requireIndex(t, tx, "grok_video_tasks", "idx_grok_video_tasks_created_at")

	var usageBillingDedupArchiveRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.usage_billing_dedup_archive')").Scan(&usageBillingDedupArchiveRegclass))
	require.True(t, usageBillingDedupArchiveRegclass.Valid, "expected usage_billing_dedup_archive table to exist")
	requireColumn(t, tx, "usage_billing_dedup_archive", "request_fingerprint", "character varying", 64, false)
	requireIndex(t, tx, "usage_billing_dedup_archive", "usage_billing_dedup_archive_pkey")

	// settings table should exist
	var settingsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.settings')").Scan(&settingsRegclass))
	require.True(t, settingsRegclass.Valid, "expected settings table to exist")

	// notification email broadcasts: durable jobs, recipient state machine, and drafts.
	var emailBroadcastJobsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.notification_email_broadcast_jobs')").Scan(&emailBroadcastJobsRegclass))
	require.True(t, emailBroadcastJobsRegclass.Valid, "expected notification_email_broadcast_jobs table to exist")
	requireColumn(t, tx, "notification_email_broadcast_jobs", "lease_expires_at", "timestamp with time zone", 0, true)
	requireIndex(t, tx, "notification_email_broadcast_jobs", "idx_notification_email_broadcast_jobs_recovery")
	requireIndex(t, tx, "notification_email_broadcast_jobs", "idx_notification_email_broadcast_one_active")
	requireConstraintDefinitionContains(t, tx, "notification_email_broadcast_jobs", "chk_notification_email_broadcast_job_status", "running", "canceling", "completed", "canceled", "interrupted")
	requireConstraintDefinitionContains(t, tx, "notification_email_broadcast_jobs", "chk_notification_email_broadcast_counts", "target_count", "sent_count", "skipped_count", "failure_count", "uncertain_count", "unsubscribed_count")
	requireColumn(t, tx, "notification_email_broadcast_recipients", "message_id", "character varying", 255, false)
	requireConstraintDefinitionContains(t, tx, "notification_email_broadcast_recipients", "chk_notification_email_broadcast_recipient_status", "pending", "sending", "sent", "skipped", "retry", "failed", "uncertain")
	requireColumn(t, tx, "notification_email_broadcast_drafts", "payload", "jsonb", 0, false)

	// web console image tasks: user-side deletion marker should not remove admin image archives
	var webConsoleImageTasksRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.web_console_image_tasks')").Scan(&webConsoleImageTasksRegclass))
	require.True(t, webConsoleImageTasksRegclass.Valid, "expected web_console_image_tasks table to exist")
	requireColumn(t, tx, "web_console_image_tasks", "user_deleted_at", "timestamp with time zone", 0, true)

	var imageTaskHistoryRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.image_task_history')").Scan(&imageTaskHistoryRegclass))
	require.True(t, imageTaskHistoryRegclass.Valid, "expected image_task_history table to exist")
	requireColumn(t, tx, "image_task_history", "task_id", "character varying", 96, false)
	requireColumn(t, tx, "image_task_history", "result_json", "jsonb", 0, true)
	requireColumn(t, tx, "image_task_history", "storage_binding_id", "character varying", 96, false)
	requireIndex(t, tx, "image_task_history", "idx_image_task_history_status_created")
	requireIndex(t, tx, "image_task_history", "idx_image_task_history_user_created")

	// security_secrets table should exist
	var securitySecretsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.security_secrets')").Scan(&securitySecretsRegclass))
	require.True(t, securitySecretsRegclass.Valid, "expected security_secrets table to exist")

	// scheduler_outbox pending dedup support
	requireColumn(t, tx, "scheduler_outbox", "dedup_key", "text", 0, true)
	requireIndex(t, tx, "scheduler_outbox", "idx_scheduler_outbox_pending_dedup_key")

	// ops_system_logs: API key id index for operational log triage
	requireColumn(t, tx, "ops_system_logs", "api_key_id", "bigint", 0, true)
	requireIndex(t, tx, "ops_system_logs", "idx_ops_system_logs_api_key_id_created_at")

	// Bounded ingress rejection security aggregates.
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "bucket_start", "timestamp with time zone", 0, false)
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "client_ip", "inet", 0, false)
	requireColumn(t, tx, "ops_ingress_reject_aggregates", "request_count", "bigint", 0, false)
	requireIndex(t, tx, "ops_ingress_reject_aggregates", "idx_ops_ingress_reject_aggregates_bucket")
	requireIndex(t, tx, "ops_ingress_reject_aggregates", "idx_ops_ingress_reject_aggregates_ip_bucket")

	// user_allowed_groups table should exist
	var uagRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.user_allowed_groups')").Scan(&uagRegclass))
	require.True(t, uagRegclass.Valid, "expected user_allowed_groups table to exist")

	// user_subscriptions: deleted_at for soft delete support (migration 012)
	requireColumn(t, tx, "user_subscriptions", "deleted_at", "timestamp with time zone", 0, true)

	// orphan_allowed_groups_audit table should exist (migration 013)
	var orphanAuditRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(context.Background(), "SELECT to_regclass('public.orphan_allowed_groups_audit')").Scan(&orphanAuditRegclass))
	require.True(t, orphanAuditRegclass.Valid, "expected orphan_allowed_groups_audit table to exist")

	// account_groups: created_at should be timestamptz
	requireColumn(t, tx, "account_groups", "created_at", "timestamp with time zone", 0, false)

	// user_allowed_groups: created_at should be timestamptz
	requireColumn(t, tx, "user_allowed_groups", "created_at", "timestamp with time zone", 0, false)
}

func TestMigrationsRunner_EnsuresIndexOnPartitionedTable(t *testing.T) {
	ctx := context.Background()
	const tableName = "migration_partitioned_usage_logs_test"
	const indexName = "migration_partitioned_usage_logs_account_id_id"

	_, err := integrationDB.ExecContext(ctx, `
		CREATE TABLE migration_partitioned_usage_logs_test (
			id BIGINT NOT NULL,
			account_id BIGINT NOT NULL
		) PARTITION BY RANGE (id);
		CREATE TABLE migration_partitioned_usage_logs_test_p1
			PARTITION OF migration_partitioned_usage_logs_test FOR VALUES FROM (MINVALUE) TO (1000);
		CREATE TABLE migration_partitioned_usage_logs_test_p2
			PARTITION OF migration_partitioned_usage_logs_test FOR VALUES FROM (1000) TO (MAXVALUE);
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS migration_partitioned_usage_logs_test CASCADE")
	})

	require.NoError(t, ensurePartitionedIndex(ctx, integrationDB, tableName, indexName, []string{"account_id", "id"}))

	var parentValid bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT i.indisvalid
		FROM pg_class idx
		JOIN pg_index i ON i.indexrelid = idx.oid
		WHERE idx.relname = $1
	`, indexName).Scan(&parentValid))
	require.True(t, parentValid)

	var attached int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pg_inherits inh
		JOIN pg_class parent_idx ON parent_idx.oid = inh.inhparent
		WHERE parent_idx.relname = $1
	`, indexName).Scan(&attached))
	require.Equal(t, 2, attached)

	// A retry after a partially or fully completed run must be a no-op.
	require.NoError(t, ensurePartitionedIndex(ctx, integrationDB, tableName, indexName, []string{"account_id", "id"}))
}

func TestMigrationsRunner_AuthIdentityAndPaymentSchemaStayAligned(t *testing.T) {
	tx := testTx(t)

	requireColumn(t, tx, "auth_identity_migration_reports", "report_type", "character varying", 80, false)
	requireColumn(t, tx, "users", "signup_source", "character varying", 20, false)
	requireColumnDefaultContains(t, tx, "users", "signup_source", "email")
	requireConstraintDefinitionContains(
		t,
		tx,
		"users",
		"users_signup_source_check",
		"signup_source",
		"'email'",
		"'linuxdo'",
		"'wechat'",
		"'oidc'",
	)

	requireForeignKeyOnDelete(t, tx, "auth_identities", "user_id", "users", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "auth_identity_channels", "identity_id", "auth_identities", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "pending_auth_sessions", "target_user_id", "users", "SET NULL")
	requireForeignKeyOnDelete(t, tx, "identity_adoption_decisions", "pending_auth_session_id", "pending_auth_sessions", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "identity_adoption_decisions", "identity_id", "auth_identities", "SET NULL")

	requireIndex(t, tx, "payment_orders", "paymentorder_out_trade_no")
	requirePartialUniqueIndexDefinition(t, tx, "payment_orders", "paymentorder_out_trade_no", "out_trade_no", "WHERE")
	requireIndexAbsent(t, tx, "payment_orders", "paymentorder_out_trade_no_unique")
}

func requireIndex(t *testing.T, tx *sql.Tx, table, index string) {
	t.Helper()

	var exists bool
	err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1
	FROM pg_indexes
	WHERE schemaname = 'public'
	  AND tablename = $1
	  AND indexname = $2
)
`, table, index).Scan(&exists)
	require.NoError(t, err, "query pg_indexes for %s.%s", table, index)
	require.True(t, exists, "expected index %s on %s", index, table)
}

func requireIndexAbsent(t *testing.T, tx *sql.Tx, table, index string) {
	t.Helper()

	var exists bool
	err := tx.QueryRowContext(context.Background(), `
SELECT EXISTS (
	SELECT 1
	FROM pg_indexes
	WHERE schemaname = 'public'
	  AND tablename = $1
	  AND indexname = $2
)
`, table, index).Scan(&exists)
	require.NoError(t, err, "query pg_indexes for %s.%s", table, index)
	require.False(t, exists, "expected index %s on %s to be absent", index, table)
}

func requirePartialUniqueIndexDefinition(t *testing.T, tx *sql.Tx, table, index string, fragments ...string) {
	t.Helper()

	var (
		unique bool
		def    string
	)

	err := tx.QueryRowContext(context.Background(), `
SELECT
	i.indisunique,
	pg_get_indexdef(i.indexrelid)
FROM pg_class idx
JOIN pg_index i ON i.indexrelid = idx.oid
JOIN pg_class tbl ON tbl.oid = i.indrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = $1
  AND idx.relname = $2
`, table, index).Scan(&unique, &def)
	require.NoError(t, err, "query index definition for %s.%s", table, index)
	require.True(t, unique, "expected index %s on %s to be unique", index, table)

	for _, fragment := range fragments {
		require.Contains(t, def, fragment, "expected index definition for %s.%s to contain %q", table, index, fragment)
	}
}

func requireForeignKeyOnDelete(t *testing.T, tx *sql.Tx, table, column, refTable, expected string) {
	t.Helper()

	var actual string
	err := tx.QueryRowContext(context.Background(), `
SELECT CASE c.confdeltype
	WHEN 'a' THEN 'NO ACTION'
	WHEN 'r' THEN 'RESTRICT'
	WHEN 'c' THEN 'CASCADE'
	WHEN 'n' THEN 'SET NULL'
	WHEN 'd' THEN 'SET DEFAULT'
END
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid = c.conrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
JOIN pg_class ref_tbl ON ref_tbl.oid = c.confrelid
JOIN pg_attribute attr ON attr.attrelid = tbl.oid AND attr.attnum = ANY(c.conkey)
WHERE ns.nspname = 'public'
  AND c.contype = 'f'
  AND tbl.relname = $1
  AND attr.attname = $2
  AND ref_tbl.relname = $3
LIMIT 1
`, table, column, refTable).Scan(&actual)
	require.NoError(t, err, "query foreign key action for %s.%s -> %s", table, column, refTable)
	require.Equal(t, expected, actual, "unexpected ON DELETE action for %s.%s -> %s", table, column, refTable)
}

func requireConstraintDefinitionContains(t *testing.T, tx *sql.Tx, table, constraint string, fragments ...string) {
	t.Helper()

	var def string
	err := tx.QueryRowContext(context.Background(), `
SELECT pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid = c.conrelid
JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
WHERE ns.nspname = 'public'
  AND tbl.relname = $1
  AND c.conname = $2
`, table, constraint).Scan(&def)
	require.NoError(t, err, "query constraint definition for %s.%s", table, constraint)

	for _, fragment := range fragments {
		require.Contains(t, def, fragment, "expected constraint definition for %s.%s to contain %q", table, constraint, fragment)
	}
}

func requireColumnDefaultContains(t *testing.T, tx *sql.Tx, table, column string, fragments ...string) {
	t.Helper()

	var columnDefault sql.NullString
	err := tx.QueryRowContext(context.Background(), `
SELECT column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&columnDefault)
	require.NoError(t, err, "query column_default for %s.%s", table, column)
	require.True(t, columnDefault.Valid, "expected column_default for %s.%s", table, column)

	for _, fragment := range fragments {
		require.Contains(t, columnDefault.String, fragment, "expected default for %s.%s to contain %q", table, column, fragment)
	}
}

func requireColumn(t *testing.T, tx *sql.Tx, table, column, dataType string, maxLen int, nullable bool) {
	t.Helper()

	var row struct {
		DataType string
		MaxLen   sql.NullInt64
		Nullable string
	}

	err := tx.QueryRowContext(context.Background(), `
SELECT
  data_type,
  character_maximum_length,
  is_nullable
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = $1
  AND column_name = $2
`, table, column).Scan(&row.DataType, &row.MaxLen, &row.Nullable)
	require.NoError(t, err, "query information_schema.columns for %s.%s", table, column)
	require.Equal(t, dataType, row.DataType, "data_type mismatch for %s.%s", table, column)

	if maxLen > 0 {
		require.True(t, row.MaxLen.Valid, "expected maxLen for %s.%s", table, column)
		require.Equal(t, int64(maxLen), row.MaxLen.Int64, "maxLen mismatch for %s.%s", table, column)
	}

	if nullable {
		require.Equal(t, "YES", row.Nullable, "nullable mismatch for %s.%s", table, column)
	} else {
		require.Equal(t, "NO", row.Nullable, "nullable mismatch for %s.%s", table, column)
	}
}
