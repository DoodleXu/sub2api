//go:build unit

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
)

type stripeRefundBackend struct {
	params     []*stripe.RefundCreateParams
	refunds    []*stripe.Refund
	rawMethods []string
	rawPaths   []string
	rawBodies  [][]byte
}

func (b *stripeRefundBackend) Call(_ string, _ string, _ string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	b.params = append(b.params, params.(*stripe.RefundCreateParams))
	refund := v.(*stripe.Refund)
	refund.ID = "re_123"
	refund.Status = stripe.RefundStatusSucceeded
	return nil
}

func (*stripeRefundBackend) CallStreaming(string, string, string, stripe.ParamsContainer, stripe.StreamingLastResponseSetter) error {
	return nil
}

func (b *stripeRefundBackend) CallRaw(method, path, _ string, body []byte, _ *stripe.Params, v stripe.LastResponseSetter) error {
	b.rawMethods = append(b.rawMethods, method)
	b.rawPaths = append(b.rawPaths, path)
	b.rawBodies = append(b.rawBodies, append([]byte(nil), body...))
	payload, err := json.Marshal(struct {
		Object  string           `json:"object"`
		Data    []*stripe.Refund `json:"data"`
		HasMore bool             `json:"has_more"`
		URL     string           `json:"url"`
	}{
		Object: "list",
		Data:   b.refunds,
		URL:    "/v1/refunds",
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

func (*stripeRefundBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *stripe.Params, stripe.LastResponseSetter) error {
	return nil
}

func (*stripeRefundBackend) SetMaxNetworkRetries(int64) {}

func TestStripeRefundUsesStableAmountSpecificIdempotencyKey(t *testing.T) {
	backend := &stripeRefundBackend{}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{
		config:      map[string]string{"currency": "CNY"},
		initialized: true,
		sc:          client,
	}

	refund := func(amount string) {
		_, err := provider.Refund(context.Background(), payment.RefundRequest{
			TradeNo: "pi_123",
			OrderID: "sub2_order_456",
			Amount:  amount,
		})
		require.NoError(t, err)
	}

	refund("12.34")
	refund("12.34")
	refund("12.35")

	require.Len(t, backend.params, 3)
	require.Equal(t, int64(1234), *backend.params[0].Amount)
	require.Equal(t, "re-sub2_order_456-1234", *backend.params[0].IdempotencyKey)
	require.Equal(t, backend.params[0].IdempotencyKey, backend.params[1].IdempotencyKey)
	require.Equal(t, int64(1235), *backend.params[2].Amount)
	require.Equal(t, "re-sub2_order_456-1235", *backend.params[2].IdempotencyKey)
	require.NotEqual(t, *backend.params[0].IdempotencyKey, *backend.params[2].IdempotencyKey)
}

func TestStripeRefundUsesAttemptMetadataAndIdempotencyKey(t *testing.T) {
	backend := &stripeRefundBackend{}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{
		config:      map[string]string{"currency": "CNY"},
		initialized: true,
		sc:          client,
	}

	_, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo:  "pi_123",
		OrderID:  "sub2_order_456",
		Amount:   "12.34",
		RefundID: "rf_attempt_123",
	})
	require.NoError(t, err)
	require.Len(t, backend.params, 1)
	require.Equal(t, "re-rf_attempt_123", *backend.params[0].IdempotencyKey)
	require.Equal(t, "rf_attempt_123", backend.params[0].Metadata["sub2api_attempt_id"])
}

func TestStripeQueryRefundByOrderMatchesExactAttemptAndAmount(t *testing.T) {
	backend := &stripeRefundBackend{refunds: []*stripe.Refund{
		{ID: "re_wrong_attempt", Amount: 1234, Metadata: map[string]string{"sub2api_attempt_id": "rf_other"}, Status: stripe.RefundStatusSucceeded},
		{ID: "re_wrong_amount", Amount: 1235, Metadata: map[string]string{"sub2api_attempt_id": "rf_attempt_123"}, Status: stripe.RefundStatusSucceeded},
		{ID: "re_exact", Amount: 1234, Metadata: map[string]string{"sub2api_attempt_id": "rf_attempt_123"}, Status: stripe.RefundStatusPending},
	}}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{
		config:      map[string]string{"currency": "CNY"},
		initialized: true,
		sc:          client,
	}

	resp, err := provider.QueryRefundByOrder(context.Background(), payment.RefundQueryRequest{
		TradeNo:   "pi_123",
		AttemptID: "rf_attempt_123",
		Amount:    "12.34",
	})
	require.NoError(t, err)
	require.Equal(t, "re_exact", resp.RefundID)
	require.Equal(t, payment.ProviderStatusPending, resp.Status)
	require.Equal(t, []string{http.MethodGet}, backend.rawMethods)
	require.Equal(t, []string{"/v1/refunds"}, backend.rawPaths)
	require.Len(t, backend.rawBodies, 1)
	query, err := url.ParseQuery(string(backend.rawBodies[0]))
	require.NoError(t, err)
	require.Equal(t, "pi_123", query.Get("payment_intent"))
	require.Equal(t, "100", query.Get("limit"))
}

func TestStripeQueryRefundByOrderRejectsAmbiguousMatches(t *testing.T) {
	backend := &stripeRefundBackend{refunds: []*stripe.Refund{
		{ID: "re_first", Amount: 1234, Metadata: map[string]string{"sub2api_attempt_id": "rf_attempt_123"}, Status: stripe.RefundStatusSucceeded},
		{ID: "re_second", Amount: 1234, Metadata: map[string]string{"sub2api_attempt_id": "rf_attempt_123"}, Status: stripe.RefundStatusSucceeded},
	}}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{
		config:      map[string]string{"currency": "CNY"},
		initialized: true,
		sc:          client,
	}

	resp, err := provider.QueryRefundByOrder(context.Background(), payment.RefundQueryRequest{
		TradeNo:   "pi_123",
		AttemptID: "rf_attempt_123",
		Amount:    "12.34",
	})
	require.Nil(t, resp)
	require.ErrorContains(t, err, "multiple refunds match attempt")
}

func TestStripeQueryRefundByOrderLegacyMatchesUniqueAmount(t *testing.T) {
	backend := &stripeRefundBackend{refunds: []*stripe.Refund{
		{ID: "re_wrong_amount", Amount: 1235, Status: stripe.RefundStatusSucceeded},
		{ID: "re_unique", Amount: 1234, Status: stripe.RefundStatusSucceeded},
	}}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{
		config:      map[string]string{"currency": "CNY"},
		initialized: true,
		sc:          client,
	}

	resp, err := provider.QueryRefundByOrder(context.Background(), payment.RefundQueryRequest{
		TradeNo: "pi_123",
		Amount:  "12.34",
	})
	require.NoError(t, err)
	require.Equal(t, "re_unique", resp.RefundID)
	require.Equal(t, payment.ProviderStatusSuccess, resp.Status)
}

func TestStripeQueryRefundByOrderLegacyFailsClosedWithoutUniqueAmount(t *testing.T) {
	tests := []struct {
		name      string
		refunds   []*stripe.Refund
		wantError string
	}{
		{
			name:      "no matching refund",
			refunds:   []*stripe.Refund{{ID: "re_other", Amount: 1235, Status: stripe.RefundStatusSucceeded}},
			wantError: "no refund matches amount",
		},
		{
			name: "ambiguous amount",
			refunds: []*stripe.Refund{
				{ID: "re_first", Amount: 1234, Status: stripe.RefundStatusSucceeded},
				{ID: "re_second", Amount: 1234, Status: stripe.RefundStatusSucceeded},
			},
			wantError: "multiple refunds match amount",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &stripeRefundBackend{refunds: tt.refunds}
			client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
			provider := &Stripe{
				config:      map[string]string{"currency": "CNY"},
				initialized: true,
				sc:          client,
			}

			resp, err := provider.QueryRefundByOrder(context.Background(), payment.RefundQueryRequest{
				TradeNo: "pi_123",
				Amount:  "12.34",
			})
			require.Nil(t, resp)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}
