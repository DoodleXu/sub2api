package service

import (
	"context"
	"testing"
	"time"

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
	byID map[int64]*UserSubscription
}

func (s *apiKeyBindingUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if sub, ok := s.byID[id]; ok {
		clone := *sub
		return &clone, nil
	}
	return nil, ErrSubscriptionNotFound
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

func TestAPIKeyGuardBlocksActiveSubscriptionSwitchToStandardGroup(t *testing.T) {
	oldSubscriptionID := int64(99)
	oldGroupID := int64(10)
	newGroupID := int64(20)
	svc := &APIKeyService{
		groupRepo: &apiKeyBindingGroupRepoStub{group: &Group{ID: newGroupID, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard}},
		userSubRepo: &apiKeyBindingUserSubRepoStub{byID: map[int64]*UserSubscription{
			oldSubscriptionID: {ID: oldSubscriptionID, UserID: 42, GroupID: oldGroupID, Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour)},
		}},
	}

	err := svc.guardSubscriptionBindingChange(context.Background(), &APIKey{ID: 7, UserID: 42, GroupID: &oldGroupID, SubscriptionID: &oldSubscriptionID}, &newGroupID, nil)
	require.ErrorIs(t, err, ErrAPIKeySubscriptionSwitchBlocked)
}
