package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type openAIRoutingExplainTestRepo struct {
	AccountRepository
	accounts []Account
}

func (r openAIRoutingExplainTestRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, *pagination.PaginationResult, error) {
	return r.accounts, &pagination.PaginationResult{Total: int64(len(r.accounts)), Page: 1, PageSize: len(r.accounts)}, nil
}

func (r openAIRoutingExplainTestRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	result := make([]*Account, 0, len(ids))
	for i := range r.accounts {
		if _, ok := seen[r.accounts[i].ID]; ok {
			result = append(result, &r.accounts[i])
		}
	}
	return result, nil
}

func (r openAIRoutingExplainTestRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, nil
}

func TestOpenAIGatewayService_ExplainOpenAIRoutingForAccount_NotFound(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true},
		}},
	}

	result, err := svc.ExplainOpenAIRoutingForAccount(context.Background(), 74002, OpenAIRoutingExplainParams{})

	require.Nil(t, result)
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_FiltersRequestedAccountIDs(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74011, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: 74012, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: 74013, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{AccountIDs: []int64{74012, 74013}, AccountIDsProvided: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	require.Equal(t, int64(74012), result.Items[0].AccountID)
}

func TestOpenAIGatewayService_ExplainOpenAIRouting_EmptyProvidedAccountIDsDoesNotFallbackToAll(t *testing.T) {
	svc := &OpenAIGatewayService{
		accountRepo: openAIRoutingExplainTestRepo{accounts: []Account{
			{ID: 74021, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
	}

	result, err := svc.ExplainOpenAIRouting(context.Background(), OpenAIRoutingExplainParams{AccountIDsProvided: true})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.Items)
}
