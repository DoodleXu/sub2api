package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type upstreamBalanceTestAccountRepo struct {
	AccountRepository
	account *Account
	updates map[string]any
}

func (r *upstreamBalanceTestAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, nil
	}
	return r.account, nil
}

func (r *upstreamBalanceTestAccountRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.updates = updates
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	return nil
}

func TestOpenAIUpstreamBalanceService_ProbeNewAPIConvertsQuotaToUSD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/usage/token/", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"quota":1000000,"used_quota":500000}}`))
	}))
	defer server.Close()

	svc := NewOpenAIUpstreamBalanceService(nil)
	updates, err := svc.probeNewAPI(context.Background(), server.URL+"/v1", "sk-test")

	require.NoError(t, err)
	require.Equal(t, UpstreamBalanceProviderNewAPI, updates["upstream_balance_provider"])
	require.Equal(t, "USD", updates["upstream_balance_unit"])
	require.Equal(t, 1.0, updates["upstream_balance_remaining"])
}

func TestOpenAIUpstreamBalanceService_ProbeNewAPIKeepsExplicitUnit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_available":42,"unit":"credits"}}`))
	}))
	defer server.Close()

	svc := NewOpenAIUpstreamBalanceService(nil)
	updates, err := svc.probeNewAPI(context.Background(), server.URL, "sk-test")

	require.NoError(t, err)
	require.Equal(t, "credits", updates["upstream_balance_unit"])
	require.Equal(t, 42.0, updates["upstream_balance_remaining"])
}

func TestOpenAIUpstreamBalanceService_RefreshClearsStaleDerivedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/usage":
			http.Error(w, `{"error":"not sub2api"}`, http.StatusNotFound)
		case "/api/usage/token/":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"total_available":250000}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &upstreamBalanceTestAccountRepo{account: &Account{
		ID:          73001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": server.URL + "/v1"},
		Extra: map[string]any{
			"upstream_balance_provider":          UpstreamBalanceProviderSub2API,
			"upstream_group":                     "old-group",
			"upstream_group_rate_multiplier":     0.08,
			"upstream_effective_rate_multiplier": 0.08,
			"upstream_rate_source":               "group_rate",
		},
	}}
	svc := NewOpenAIUpstreamBalanceService(repo)

	account, err := svc.Refresh(context.Background(), repo.account.ID)

	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, UpstreamBalanceProviderNewAPI, account.Extra["upstream_balance_provider"])
	require.Equal(t, 0.5, account.Extra["upstream_balance_remaining"])
	require.Nil(t, account.Extra["upstream_group"])
	require.Nil(t, account.Extra["upstream_group_rate_multiplier"])
	require.Nil(t, account.Extra["upstream_effective_rate_multiplier"])
	require.Nil(t, account.Extra["upstream_rate_source"])
	require.Contains(t, repo.updates, "upstream_effective_rate_multiplier")
}

func TestOpenAIUpstreamBalanceService_RefreshFailureClearsStaleDerivedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unavailable"}`, http.StatusBadGateway)
	}))
	defer server.Close()

	repo := &upstreamBalanceTestAccountRepo{account: &Account{
		ID:          73002,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": server.URL + "/v1"},
		Extra: map[string]any{
			"upstream_balance_provider":          UpstreamBalanceProviderSub2API,
			"upstream_group":                     "old-group",
			"upstream_group_rate_multiplier":     0.08,
			"upstream_effective_rate_multiplier": 0.08,
			"upstream_rate_source":               "group_rate",
		},
	}}
	svc := NewOpenAIUpstreamBalanceService(repo)

	account, err := svc.Refresh(context.Background(), repo.account.ID)

	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, UpstreamBalanceStatusError, account.Extra["upstream_balance_status"])
	require.NotEmpty(t, account.Extra["upstream_balance_error"])
	require.Nil(t, account.Extra["upstream_group"])
	require.Nil(t, account.Extra["upstream_group_rate_multiplier"])
	require.Nil(t, account.Extra["upstream_effective_rate_multiplier"])
	require.Nil(t, account.Extra["upstream_rate_source"])
	require.Contains(t, repo.updates, "upstream_effective_rate_multiplier")
}
