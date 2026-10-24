//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountBalanceSafetyMigrationAndManualRecovery(t *testing.T) {
	ctx := context.Background()
	var id int64
	err := integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,extra,notes) VALUES('balance-safety-test','openai','apikey','{"api_key":"test-only"}','{"keep":"yes","openai_continue_scheduling_after_limit":true}','管理员备注') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", id)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", id)
	})
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	a, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	safety := service.NewRateLimitService(repo, nil, nil, nil, nil)
	require.NoError(t, safety.ValidateAccountDispatch(ctx, a), "real dispatch projection accepts the current account")
	require.NotContains(t, a.Extra, "openai_continue_scheduling_after_limit")
	require.Equal(t, "yes", a.Extra["keep"])
	pause := service.AccountBalancePause{Reason: "insufficient_account_balance", Message: "Insufficient account balance", At: time.Now(), Note: "[自动停调·余额不足] 测试"}
	applied, err := repo.PauseAccountForInsufficientBalance(ctx, a, pause)
	require.NoError(t, err)
	require.True(t, applied)
	require.Error(t, safety.ValidateAccountDispatch(ctx, a), "the projection must observe a financial pause")
	applied, err = repo.PauseAccountForInsufficientBalance(ctx, a, pause)
	require.NoError(t, err)
	require.False(t, applied)
	paused, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.False(t, paused.Schedulable)
	require.Contains(t, *paused.Notes, "管理员备注")
	require.Equal(t, 1, strings.Count(*paused.Notes, pause.Note))
	// A stale general edit or background refresh cannot lift a financial pause.
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true,extra='{"keep":"updated"}',notes='编辑后的备注' WHERE id=$1`, id)
	require.NoError(t, err)
	paused, err = repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.False(t, paused.Schedulable)
	require.NotNil(t, paused.Extra[service.AccountBalancePauseKey])
	require.Contains(t, *paused.Notes, pause.Note)
	require.NoError(t, repo.SetSchedulable(ctx, id, true))
	recovered, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.True(t, recovered.Schedulable)
	require.NotContains(t, recovered.Extra, service.AccountBalancePauseKey)
	require.Greater(t, service.AccountSchedulingGeneration(recovered), service.AccountSchedulingGeneration(a))
	require.Error(t, safety.ValidateAccountDispatch(ctx, a), "an old request remains fenced after admin resume")
	require.NoError(t, safety.ValidateAccountDispatch(ctx, recovered))
	applied, err = repo.PauseAccountForInsufficientBalance(ctx, a, pause)
	require.NoError(t, err)
	require.False(t, applied, "old in-flight error must not undo manual recovery")
	// A credential replacement similarly invalidates older observations.
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials='{"api_key":"replacement-test-only"}' WHERE id=$1`, id)
	require.NoError(t, err)
	require.Error(t, safety.ValidateAccountDispatch(ctx, recovered), "credential replacement invalidates the projected generation")
	applied, err = repo.PauseAccountForInsufficientBalance(ctx, recovered, pause)
	require.NoError(t, err)
	require.False(t, applied)
	raw, err := json.Marshal(recovered.Extra)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "scheduler_manual_resume")
}
