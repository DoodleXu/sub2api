package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountCostDeltaMigrationAvoidsRoutineFullRebuilds(t *testing.T) {
	content, err := FS.ReadFile("229_usage_account_cost_delta_updates.sql")
	require.NoError(t, err)
	sql := string(content)

	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION apply_account_cost_delta")
	require.Contains(t, sql, "published_account_cost = published_account_cost + account_cost_delta")
	require.Contains(t, sql, "usage_id <= last_processed_usage_id")
	require.Contains(t, sql, "NEW.total_cost - OLD.total_cost")
	require.Contains(t, sql, "FROM accounts")
	require.Contains(t, sql, "usage_account_cost_dirty_buckets")
	require.Contains(t, sql, "trg_accounts_cost_total_initialize")
	require.Contains(t, sql, "initialized = FALSE")
}

func TestRemoveForkCostLedgersMigrationDropsAllForkStructures(t *testing.T) {
	content, err := FS.ReadFile("241_remove_fork_cost_ledgers.sql")
	require.NoError(t, err)
	sql := string(content)
	for _, name := range []string{
		"usage_dashboard_cost_snapshot",
		"usage_dashboard_account_cost_daily",
		"usage_dashboard_account_cost_hourly",
		"usage_account_cost_dirty_buckets",
		"usage_account_cost_totals",
	} {
		require.Contains(t, sql, "DROP TABLE IF EXISTS "+name)
	}
	for _, name := range []string{
		"trg_usage_logs_account_cost_pending",
		"trg_usage_logs_account_cost_rebuild",
		"trg_accounts_cost_total_initialize",
	} {
		require.Contains(t, sql, "DROP TRIGGER IF EXISTS "+name)
	}
}
