package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

const parameterLimitTestDriverName = "sub2api_param_limit_test"

func TestAccountEntityToServiceCarriesParentArchiveState(t *testing.T) {
	archivedAt := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	parentID := int64(41)
	child := &dbent.Account{ID: 42, ParentAccountID: &parentID}
	child.Edges.Parent = &dbent.Account{ID: parentID, ArchivedAt: &archivedAt}

	got := accountEntityToService(child)
	require.NotNil(t, got)
	require.Equal(t, &archivedAt, got.ParentArchivedAt)
	require.True(t, got.IsArchived())
}

var registerParameterLimitTestDriverOnce sync.Once
var parameterLimitQueryCount atomic.Int64

func TestAccountsToService_LargeActiveAccountSetDoesNotExceedPostgresParameterLimit(t *testing.T) {
	repo := newParameterLimitAccountRepo(t)
	parameterLimitQueryCount.Store(0)

	accounts := make([]*dbent.Account, 0, 65536)
	for i := range 65536 {
		parentID := int64(1000000 + i)
		accounts = append(accounts, &dbent.Account{
			ID:              int64(i + 1),
			Name:            "large-active",
			Platform:        service.PlatformOpenAI,
			Type:            service.AccountTypeOAuth,
			Credentials:     map[string]any{},
			Extra:           map[string]any{},
			Status:          service.StatusActive,
			Schedulable:     true,
			ParentAccountID: &parentID,
		})
	}

	got, err := repo.accountsToService(context.Background(), accounts)
	require.NoError(t, err)
	require.Len(t, got, len(accounts))
	require.LessOrEqual(t, parameterLimitQueryCount.Load(), int64(6), "parent and group state must be loaded in bounded batches")
}

func TestGetByIDs_LargeSetDoesNotExceedPostgresParameterLimit(t *testing.T) {
	repo := newParameterLimitAccountRepo(t)
	parameterLimitQueryCount.Store(0)
	ids := make([]int64, 65536)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	got, err := repo.GetByIDs(context.Background(), ids)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, int64(2), parameterLimitQueryCount.Load(), "the entry query must split IDs at the repository batch boundary")
}

func TestListShadowsByParents_LargeSetDoesNotExceedPostgresParameterLimit(t *testing.T) {
	repo := newParameterLimitAccountRepo(t)
	parameterLimitQueryCount.Store(0)
	parentIDs := make([]int64, 65536)
	for i := range parentIDs {
		parentIDs[i] = int64(i + 1)
	}

	got, err := repo.ListShadowsByParents(context.Background(), parentIDs)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, int64(2), parameterLimitQueryCount.Load(), "shadow lookup must split parent IDs at the repository batch boundary")
}

func TestListShadowsByParentsReturnsRequestedSparkShadows(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	repo := newAccountRepositoryWithSQL(client, nil, nil)

	createAccount := func(name string, parentID *int64) *dbent.Account {
		builder := client.Account.Create().
			SetName(name).
			SetPlatform(service.PlatformOpenAI).
			SetType(service.AccountTypeOAuth).
			SetStatus(service.StatusActive).
			SetCredentials(map[string]any{})
		if parentID != nil {
			builder.SetParentAccountID(*parentID).
				SetQuotaDimension(dbaccount.QuotaDimensionSpark)
		}
		account, err := builder.Save(ctx)
		require.NoError(t, err)
		return account
	}

	parent1 := createAccount("batch-shadow-parent-1", nil)
	parent2 := createAccount("batch-shadow-parent-2", nil)
	parent3 := createAccount("batch-shadow-parent-3", nil)
	shadow1 := createAccount("batch-shadow-1", &parent1.ID)
	shadow2 := createAccount("batch-shadow-2", &parent2.ID)
	_ = createAccount("batch-shadow-unrequested", &parent3.ID)

	got, err := repo.ListShadowsByParents(ctx, []int64{parent2.ID, parent1.ID, parent2.ID, 0})
	require.NoError(t, err)
	gotIDs := make([]int64, 0, len(got))
	for _, account := range got {
		gotIDs = append(gotIDs, account.ID)
	}
	require.ElementsMatch(t, []int64{shadow1.ID, shadow2.ID}, gotIDs)
}

func TestGetByIDReadsParentArchiveStateFromCallerTransaction(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	parent, err := client.Account.Create().
		SetName("transaction-parent").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetStatus(service.StatusActive).
		SetCredentials(map[string]any{}).
		Save(ctx)
	require.NoError(t, err)
	child, err := client.Account.Create().
		SetName("transaction-shadow").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetStatus(service.StatusActive).
		SetCredentials(map[string]any{}).
		SetParentAccountID(parent.ID).
		Save(ctx)
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQL(client, nil, nil)

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	archivedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	_, err = tx.Client().Account.UpdateOneID(parent.ID).SetArchivedAt(archivedAt).Save(txCtx)
	require.NoError(t, err)

	got, err := repo.GetByID(txCtx, child.ID)
	require.NoError(t, err)
	require.Equal(t, &archivedAt, got.ParentArchivedAt)
	require.True(t, got.IsArchived())
	require.NoError(t, tx.Rollback())
}

func newParameterLimitAccountRepo(t *testing.T) *accountRepository {
	t.Helper()

	registerParameterLimitTestDriverOnce.Do(func() {
		sql.Register(parameterLimitTestDriverName, parameterLimitDriver{})
	})

	db, err := sql.Open(parameterLimitTestDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })

	return newAccountRepositoryWithSQL(client, nil, nil)
}

type parameterLimitDriver struct{}

func (parameterLimitDriver) Open(string) (driver.Conn, error) {
	return parameterLimitConn{}, nil
}

type parameterLimitConn struct{}

func (parameterLimitConn) Prepare(query string) (driver.Stmt, error) {
	return parameterLimitStmt{query: query}, nil
}

func (parameterLimitConn) Close() error {
	return nil
}

func (parameterLimitConn) Begin() (driver.Tx, error) {
	return parameterLimitTx{}, nil
}

func (parameterLimitConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return queryWithParameterLimit(query, args)
}

type parameterLimitStmt struct {
	query string
}

func (s parameterLimitStmt) Close() error {
	return nil
}

func (s parameterLimitStmt) NumInput() int {
	return -1
}

func (s parameterLimitStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), parameterLimitError(len(args))
}

func (s parameterLimitStmt) Query(args []driver.Value) (driver.Rows, error) {
	namedArgs := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		namedArgs[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return queryWithParameterLimit(s.query, namedArgs)
}

type parameterLimitTx struct{}

func (parameterLimitTx) Commit() error {
	return nil
}

func (parameterLimitTx) Rollback() error {
	return nil
}

func queryWithParameterLimit(query string, args []driver.NamedValue) (driver.Rows, error) {
	parameterLimitQueryCount.Add(1)
	if err := parameterLimitError(len(args)); err != nil {
		return nil, err
	}
	return parameterLimitRows{columns: columnsForParameterLimitQuery(query)}, nil
}

func parameterLimitError(paramCount int) error {
	if paramCount <= 65535 {
		return nil
	}
	return fmt.Errorf("pq: got %d parameters but PostgreSQL only supports 65535 parameters", paramCount)
}

func columnsForParameterLimitQuery(query string) []string {
	if query == "" {
		return nil
	}
	return []string{"account_id", "group_id", "priority", "created_at"}
}

type parameterLimitRows struct {
	columns []string
}

func (r parameterLimitRows) Columns() []string {
	return r.columns
}

func (parameterLimitRows) Close() error {
	return nil
}

func (parameterLimitRows) Next([]driver.Value) error {
	return io.EOF
}
