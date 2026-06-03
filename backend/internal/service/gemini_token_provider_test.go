//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGeminiTokenProvider_ArchivedAccount(t *testing.T) {
	provider := NewGeminiTokenProvider(nil, nil, nil)
	now := time.Now()
	account := &Account{
		ID:         104,
		Platform:   PlatformGemini,
		Type:       AccountTypeOAuth,
		ArchivedAt: &now,
	}

	token, err := provider.GetAccessToken(context.Background(), account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "account is archived")
	require.Empty(t, token)
}
