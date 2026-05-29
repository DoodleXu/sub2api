package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type apiKeyBindingGroupRepoStub struct {
	groupRepoNoop
	group *Group
}

func (s *apiKeyBindingGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	if s.group == nil {
		return nil, ErrGroupNotFound
	}
	clone := *s.group
	return &clone, nil
}

type apiKeyBindingUserSubRepoStub struct {
	userSubRepoNoop
	subs []UserSubscription
}

func (s *apiKeyBindingUserSubRepoStub) List(_ context.Context, _ pagination.PaginationParams, _ *int64, _ *int64, _ string, _ string, _ string, _ string) ([]UserSubscription, *pagination.PaginationResult, error) {
	out := append([]UserSubscription(nil), s.subs...)
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: 2, Pages: 1}, nil
}

func TestAPIKeyResolveBindingSubscriptionGroupAutoResolvesSingleActiveSubscription(t *testing.T) {
	groupID := int64(10)
	svc := &APIKeyService{
		groupRepo: &apiKeyBindingGroupRepoStub{group: &Group{ID: groupID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}},
		userSubRepo: &apiKeyBindingUserSubRepoStub{subs: []UserSubscription{
			{ID: 99, UserID: 42, GroupID: groupID, Status: SubscriptionStatusActive},
		}},
	}

	resolvedGroupID, resolvedSubscriptionID, err := svc.resolveAPIKeyBinding(context.Background(), &User{ID: 42}, &groupID, nil)

	require.NoError(t, err)
	require.NotNil(t, resolvedGroupID)
	require.Equal(t, groupID, *resolvedGroupID)
	require.NotNil(t, resolvedSubscriptionID)
	require.Equal(t, int64(99), *resolvedSubscriptionID)
}

func TestAPIKeyResolveBindingSubscriptionGroupRequiresSubscriptionWhenNoActiveSubscription(t *testing.T) {
	groupID := int64(10)
	svc := &APIKeyService{
		groupRepo:   &apiKeyBindingGroupRepoStub{group: &Group{ID: groupID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}},
		userSubRepo: &apiKeyBindingUserSubRepoStub{},
	}

	_, _, err := svc.resolveAPIKeyBinding(context.Background(), &User{ID: 42}, &groupID, nil)

	require.ErrorIs(t, err, ErrSubscriptionRequiredForAPIKey)
}

func TestAPIKeyResolveBindingSubscriptionGroupRejectsAmbiguousActiveSubscriptions(t *testing.T) {
	groupID := int64(10)
	svc := &APIKeyService{
		groupRepo: &apiKeyBindingGroupRepoStub{group: &Group{ID: groupID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}},
		userSubRepo: &apiKeyBindingUserSubRepoStub{subs: []UserSubscription{
			{ID: 99, UserID: 42, GroupID: groupID, Status: SubscriptionStatusActive},
			{ID: 100, UserID: 42, GroupID: groupID, Status: SubscriptionStatusActive},
		}},
	}

	_, _, err := svc.resolveAPIKeyBinding(context.Background(), &User{ID: 42}, &groupID, nil)

	require.ErrorIs(t, err, ErrSubscriptionBindingAmbiguous)
}
