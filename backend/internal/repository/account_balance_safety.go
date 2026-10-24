package repository

import (
	"context"
	"encoding/json"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetAccountDispatchState reads only the fields needed by the financial safety
// boundary. Do not hydrate groups or proxies, or use this projection for edits.
func (r *accountRepository) GetAccountDispatchState(ctx context.Context, id int64) (*service.Account, error) {
	m, err := clientFromContext(ctx, r.client).Account.Query().Where(dbaccount.IDEQ(id)).Select(
		dbaccount.FieldID, dbaccount.FieldPlatform, dbaccount.FieldType,
		dbaccount.FieldStatus, dbaccount.FieldSchedulable, dbaccount.FieldArchivedAt,
		dbaccount.FieldExpiresAt, dbaccount.FieldAutoPauseOnExpired,
		dbaccount.FieldRateLimitResetAt, dbaccount.FieldOverloadUntil,
		dbaccount.FieldTempUnschedulableUntil, dbaccount.FieldExtra,
		dbaccount.FieldCredentials, dbaccount.FieldProxyID, dbaccount.FieldParentAccountID,
	).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrAccountNotFound, nil)
	}
	return accountEntityToService(m), nil
}

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
