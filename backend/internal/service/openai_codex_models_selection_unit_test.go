//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexModelsCredentialErrorRepo struct {
	*mockAccountRepoForPlatform
	parent      *Account
	parentCalls int
}

func (r *codexModelsCredentialErrorRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.parent != nil && id == r.parent.ID {
		r.parentCalls++
		if r.parentCalls <= 2 {
			return r.parent, nil
		}
		return nil, errors.New("parent repository unavailable")
	}
	return r.mockAccountRepoForPlatform.GetByID(ctx, id)
}

func TestSelectCodexModelsAccountSkipsAPIKeyAndMissingTokenAccounts(t *testing.T) {
	accounts := []Account{
		{
			ID: 1, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
			Credentials: map[string]any{"access_token": "not-a-codex-oauth-token"},
		},
		{
			ID: 2, Name: "oauth-missing-token", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
			Credentials: map[string]any{},
		},
		{
			ID: 3, Name: "oauth-ready", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Priority: 3, Concurrency: 1,
			Credentials: map[string]any{"access_token": "codex-token"},
		},
	}
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: make(map[int64]*Account)}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}

	svc := &OpenAIGatewayService{accountRepo: repo, cfg: testConfig()}
	account, err := svc.SelectCodexModelsAccount(context.Background(), nil)

	require.NoError(t, err)
	require.Equal(t, int64(3), account.ID)
}

func TestSelectCodexModelsAccountReturnsUnavailableWithoutEligibleOAuth(t *testing.T) {
	accounts := []Account{
		{
			ID: 1, Name: "api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
			Credentials: map[string]any{"api_key": "test"},
		},
	}
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{1: &accounts[0]}}

	svc := &OpenAIGatewayService{accountRepo: repo, cfg: testConfig()}
	account, err := svc.SelectCodexModelsAccount(context.Background(), nil)

	require.Error(t, err)
	require.Nil(t, account)
	require.True(t, errors.Is(err, ErrNoAvailableCodexModelsAccount))
}

func TestSelectCodexModelsAccountPropagatesShadowCredentialLookupError(t *testing.T) {
	parentID := int64(99)
	accounts := []Account{
		{
			ID: 1, Name: "shadow", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
			ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark,
		},
	}
	parent := &Account{
		ID: parentID, Name: "parent", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "parent-token"},
	}
	baseRepo := &mockAccountRepoForPlatform{
		accounts:     accounts,
		accountsByID: map[int64]*Account{1: &accounts[0], parentID: parent},
	}
	repo := &codexModelsCredentialErrorRepo{mockAccountRepoForPlatform: baseRepo, parent: parent}

	svc := &OpenAIGatewayService{accountRepo: repo, cfg: testConfig()}
	account, err := svc.SelectCodexModelsAccount(context.Background(), nil)

	require.Error(t, err)
	require.Nil(t, account)
	require.False(t, errors.Is(err, ErrNoAvailableCodexModelsAccount), "parent lookups=%d err=%v", repo.parentCalls, err)
	require.Contains(t, err.Error(), "resolve credential account")
}
