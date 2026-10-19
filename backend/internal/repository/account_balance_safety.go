package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) PauseAccountForInsufficientBalance(ctx context.Context, a *service.Account, pause service.AccountBalancePause) (bool, error) {
	payload, err := json.Marshal(pause)
	if err != nil {
		return false, err
	}
	credentials, err := json.Marshal(a.Credentials)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET schedulable=false,
 notes=concat_ws(E'\n', NULLIF(notes,''), $4::text),
 extra=jsonb_set(COALESCE(extra,'{}'::jsonb), '{scheduler_balance_pause}', $3::jsonb), updated_at=NOW()
 WHERE id=$1 AND deleted_at IS NULL AND archived_at IS NULL
 AND NOT (extra ? 'scheduler_balance_pause')
 AND COALESCE((extra->>'scheduler_generation')::bigint,0)=$2
 AND credentials=$5::jsonb`, a.ID, service.AccountSchedulingGeneration(a), string(payload), pause.Note, string(credentials))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &a.ID, nil, nil); err != nil {
			return true, err
		}
		r.syncSchedulerAccountSnapshotDetached(ctx, a.ID)
	}
	return n > 0, nil
}
