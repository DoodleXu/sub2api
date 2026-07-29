//go:build unit

package service

import (
	"context"
	"html"
	"testing"

	"github.com/stretchr/testify/require"
)

type apiKeyNameRepoStub struct {
	*apiKeyRepoStub
	created *APIKey
	updated *APIKey
	fields  APIKeyUpdateFields
	exists  bool
}

func (s *apiKeyNameRepoStub) Create(_ context.Context, key *APIKey) error {
	clone := *key
	s.created = &clone
	return nil
}

func (s *apiKeyNameRepoStub) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	clone := *key
	s.updated = &clone
	s.fields = fields
	return nil
}

func (s *apiKeyNameRepoStub) ExistsByKey(context.Context, string) (bool, error) {
	return s.exists, nil
}

type apiKeyNameUserRepoStub struct {
	*userRepoStubForGroupUpdate
	user *User
}

func (s *apiKeyNameUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	clone := *s.user
	return &clone, nil
}

func TestAPIKeyService_CreatePreservesRawName(t *testing.T) {
	rawName := `A&B <script>alert("x")</script>`
	customKey := "custom-key-123456"
	repo := &apiKeyNameRepoStub{apiKeyRepoStub: &apiKeyRepoStub{}}
	svc := &APIKeyService{
		apiKeyRepo: repo,
		userRepo: &apiKeyNameUserRepoStub{
			userRepoStubForGroupUpdate: &userRepoStubForGroupUpdate{},
			user:                       &User{ID: 42, Status: StatusActive},
		},
	}

	apiKey, err := svc.Create(context.Background(), 42, CreateAPIKeyRequest{
		Name:      rawName,
		CustomKey: &customKey,
	})

	require.NoError(t, err)
	require.Equal(t, rawName, apiKey.Name)
	require.NotNil(t, repo.created)
	require.Equal(t, rawName, repo.created.Name)
}

func TestAPIKeyService_UpdateEscapesName(t *testing.T) {
	rawName := `A&B <b>ok</b>`
	escapedName := html.EscapeString(rawName)
	repo := &apiKeyNameRepoStub{
		apiKeyRepoStub: &apiKeyRepoStub{
			apiKey: &APIKey{ID: 7, UserID: 42, Key: "custom-key-123456", Name: "old", Status: StatusActive},
		},
	}
	svc := &APIKeyService{apiKeyRepo: repo}

	apiKey, err := svc.Update(context.Background(), 7, 42, UpdateAPIKeyRequest{Name: &rawName})

	require.NoError(t, err)
	require.Equal(t, escapedName, apiKey.Name)
	require.NotNil(t, repo.updated)
	require.Equal(t, escapedName, repo.updated.Name)
	require.True(t, repo.fields.Name)
}
