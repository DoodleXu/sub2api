//go:build unit

package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func healthyGrokOAuthGatewayTestAccount(id int64, token string) *Account {
	return &Account{
		ID: id, Name: "grok", Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": token, "refresh_token": "refresh-token",
			"expires_at": time.Now().Add(2 * grokTokenRefreshSkew).UTC().Format(time.RFC3339),
			"base_url":   xai.DefaultCLIBaseURL,
		},
	}
}
