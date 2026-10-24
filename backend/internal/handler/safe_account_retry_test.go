package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSafeAccountRetryRespectsFinancialAndReplayBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failure      service.UpstreamFailoverError
		count, limit int
		want         bool
	}{
		{"capacity", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, RequestScopedTransient: true}, 0, 2, true},
		{"not replayable", service.UpstreamFailoverError{StatusCode: 503}, 0, 2, false},
		{"zero budget", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}, 0, 0, false},
		{"budget exhausted", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}, 2, 2, false},
		{"error cap", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, SameAccountRetryMax: 1}, 1, 5, false},
		{"debt", service.UpstreamFailoverError{StatusCode: 402, RetryableOnSameAccount: true, ResponseBody: []byte(`{"error":{"code":"insufficient_balance"}}`)}, 0, 2, false},
		{"auth", service.UpstreamFailoverError{StatusCode: 401, RetryableOnSameAccount: true}, 0, 2, false},
		{"credential", service.UpstreamFailoverError{StatusCode: 502, RetryableOnSameAccount: true, Stage: service.GatewayFailureStageAccountAuth}, 0, 2, false},
		{"expired", service.UpstreamFailoverError{StatusCode: 429, RetryableOnSameAccount: true, SameAccountRetryDeadline: time.Now().Add(-time.Second)}, 0, 2, false},
		{"cannot wait within deadline", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, SameAccountRetryDelay: time.Second, SameAccountRetryDeadline: time.Now().Add(100 * time.Millisecond)}, 0, 2, false},
		{"long retry after", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, SameAccountRetryDelay: 30 * time.Second}, 0, 2, false},
		{"do not replay", service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, NextAccountAction: service.NextAccountStop}, 0, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sameAccountRetryAllowed(&tc.failure, tc.count, tc.limit))
		})
	}
}
func TestSafeRetryThenFailoverKeepsExclusionsAndCacheBilling(t *testing.T) {
	fs := NewFailoverState(2, true)
	failure := &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, RetryableOnSameAccount: true, RequestScopedTransient: true, SameAccountRetryDelay: time.Millisecond}
	gateway := &mockTempUnscheduler{}
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), gateway, 1, service.PlatformOpenAI, 1, failure))
	require.Zero(t, fs.SwitchCount)
	require.Empty(t, fs.FailedAccountIDs)
	require.False(t, fs.ForceCacheBilling)
	require.Equal(t, FailoverContinue, fs.HandleFailoverError(context.Background(), gateway, 1, service.PlatformOpenAI, 1, failure))
	require.Equal(t, 1, fs.SwitchCount)
	require.Contains(t, fs.FailedAccountIDs, int64(1))
	require.True(t, fs.ForceCacheBilling)
	require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(context.Background()))
	require.Contains(t, fs.FailedAccountIDs, int64(1))
}
func TestSafeAccountRetryCancellationStopsWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	account := &service.Account{ID: 1, Type: service.AccountTypeAPIKey}
	retry, canceled := retrySafeAccount(ctx, map[int64]int{}, account, &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true, SameAccountRetryDelay: time.Second})
	require.False(t, retry)
	require.True(t, canceled)
}

func TestSafeAccountRetryHonorsRetryAfterWithoutUnboundedWait(t *testing.T) {
	failure := &service.UpstreamFailoverError{StatusCode: 429, RetryableOnSameAccount: true, SameAccountRetryDelay: time.Millisecond, ResponseHeaders: http.Header{"Retry-After": []string{"2"}}}
	require.Equal(t, 2*time.Second, sameAccountRetryDelayFor(failure, 1))
	failure.ResponseHeaders.Set("Retry-After", "30")
	require.False(t, sameAccountRetryAllowed(failure, 0, 3), "long Retry-After must fail over instead of retrying early")
}
