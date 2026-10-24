package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountBalanceClassifierOnlyExplicitAccountDebt(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{403, `{"error":{"message":"Insufficient account balance"}}`, true},
		{403, `{"message":"insufficient balance"}`, true},
		{402, `{"error":{"message":"Sorry, your account balance is insufficient"}}`, true},
		{400, `{"error":{"message":"Your credit balance is too low to access the Anthropic API."}}`, true},
		{403, `{"error":{"code":"insufficient_balance"}}`, true},
		{429, `{"error":{"message":"quota exceeded"}}`, false},
		{403, `{"error":{"message":"usage limit, quota will reset"}}`, false},
		{402, `{"error":{"message":"Upstream image account has insufficient balance"}}`, false},
		{403, `{"error":{"message":"insufficient balance for model wallet"}}`, false},
		{400, `{"error":{"message":"invalid request"},"request":{"input":"Insufficient account balance"}}`, false},
		{200, `{"output":"Insufficient account balance"}`, false},
		{200, `{"message":"Insufficient account balance"}`, false},
		{200, `Insufficient account balance`, false},
		{403, `{"error":{"code":"insufficient_balance","scope":"model"}}`, false},
		{403, `{"error":{"message":"insufficient balance","param":"model"}}`, false},
		{403, `{"error":{"message":"forbidden"}}`, false},
		{402, `Payment required`, false},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			_, got := ClassifyAccountBalanceError(tc.status, []byte(tc.body))
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAccountBalanceSummaryNeverPersistsUpstreamSecrets(t *testing.T) {
	message, ok := ClassifyAccountBalanceError(403, []byte(`{"error":{"message":"Insufficient account balance; private-value-here sk-test-secret api_key=another-secret"}}`))
	require.True(t, ok)
	require.Equal(t, "Insufficient account balance", message)
}
