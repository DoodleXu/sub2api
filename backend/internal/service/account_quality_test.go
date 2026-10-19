package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
func qualitySamples(now time.Time, n, fail int, latency int64) AccountQualityState {
	st := AccountQualityState{}
	for i := 0; i < n; i++ {
		st.Samples = append(st.Samples, AccountQualitySample{ID: fmt.Sprint(i), At: now.Add(-time.Duration(n-i) * time.Second).UnixMilli(), Failed: i < fail, LatencyMS: latency})
	}
	return st
}
func TestAccountQualityWindowsAndNoOptimisticColdStart(t *testing.T) {
	now := time.Now()
	require.Equal(t, "unknown", SummarizeAccountQuality(qualitySamples(now, 19, 0, 100), now).State)
	require.Equal(t, "healthy", SummarizeAccountQuality(qualitySamples(now, 20, 1, 100), now).State)
	require.Equal(t, "degraded", SummarizeAccountQuality(qualitySamples(now, 20, 4, 100), now).State)
	require.Equal(t, "poor", SummarizeAccountQuality(qualitySamples(now, 20, 5, 100), now).State)
	st := qualitySamples(now.Add(-time.Hour), 30, 0, 2000)
	summary := SummarizeAccountQuality(st, now)
	require.Equal(t, "24h", summary.Window)
	require.EqualValues(t, 2000, *summary.P50MS)
	require.Equal(t, "unknown", SummarizeAccountQuality(st, now.Add(25*time.Hour)).State)
}
func TestAccountQualityCircuitRecoveryAndDedup(t *testing.T) {
	now := time.Now()
	st := AccountQualityState{}
	for i := 0; i < 3; i++ {
		v := AccountQualitySample{ID: fmt.Sprint(i), At: now.Add(time.Duration(i) * time.Second).UnixMilli(), Failed: true, Transient: true}
		st = AdvanceAccountQualityState(st, v)
		st = AdvanceAccountQualityState(st, v)
	}
	require.Len(t, st.Samples, 3)
	require.Equal(t, 1, st.Trips)
	require.Equal(t, "open", SummarizeAccountQuality(st, now).State)
	retryAt := time.UnixMilli(st.OpenUntil + 1)
	st = AdvanceAccountQualityState(st, AccountQualitySample{ID: "recovery1", RecoveryEpoch: st.Trips, At: retryAt.UnixMilli()})
	require.Equal(t, 1, st.Trips)
	st = AdvanceAccountQualityState(st, AccountQualitySample{ID: "recovery2", RecoveryEpoch: st.Trips, At: retryAt.Add(time.Second).UnixMilli()})
	require.Zero(t, st.Trips)
	for i := 0; i < 250; i++ {
		st = AdvanceAccountQualityState(st, AccountQualitySample{ID: fmt.Sprint("good", i), At: retryAt.Add(time.Duration(i+2) * time.Second).UnixMilli()})
	}
	require.Len(t, st.Samples, 200)
}
func TestQualityRanksReliabilityBeforeSpeedAndStaticPriority(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model", Stream: true})
	q := qualityRequest(ctx, "")
	a := &Account{ID: 1, Platform: PlatformOpenAI, Priority: 1}
	b := &Account{ID: 2, Platform: PlatformOpenAI, Priority: 100}
	c := &Account{ID: 3, Platform: PlatformOpenAI, Priority: 0}
	now := time.Now()
	svc.quality.states[AccountQualityBucket(a, q)] = cachedQualityState{qualitySamples(now, 40, 20, 100), now}
	svc.quality.states[AccountQualityBucket(b, q)] = cachedQualityState{qualitySamples(now, 40, 0, 4000), now}
	ranked := svc.RankQualityAccounts(ctx, "model", []*Account{a, b, c}, 1)
	require.Equal(t, b.ID, ranked[0].ID)
	require.Equal(t, c.ID, ranked[1].ID)
}
func TestQualityErrorAttributionExcludesClientAndModelLimits(t *testing.T) {
	for _, err := range []error{context.Canceled, &UpstreamFailoverError{StatusCode: 429}, &UpstreamFailoverError{StatusCode: 400}, &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeRequest}} {
		eligible, _ := classifyAccountQualityFailure(err)
		require.False(t, eligible)
	}
	eligible, transient := classifyAccountQualityFailure(&UpstreamFailoverError{StatusCode: 502})
	require.True(t, eligible)
	require.True(t, transient)
}

func TestNestedQualityAdaptersReportOneOutcome(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model", Stream: true})
	a := &Account{ID: 9, Platform: PlatformOpenAI}
	nested, attempt, err := svc.BeginAccountQualityAttempt(ctx, a)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	_, child, err := svc.BeginAccountQualityAttempt(nested, a)
	require.NoError(t, err)
	require.Nil(t, child)
	first := 120
	result := &OpenAIForwardResult{UpstreamModel: "model", FirstTokenMs: &first}
	require.NoError(t, attempt.Finish(result, nil))
	require.NoError(t, attempt.Finish(result, nil))
	attempt.Abort()
	st := svc.quality.states[AccountQualityBucket(a, qualityRequest(ctx, ""))].state
	require.Len(t, st.Samples, 1)
	require.EqualValues(t, 120, st.Samples[0].LatencyMS)
	require.Empty(t, svc.quality.leases)
}
func TestOldInFlightSuccessDoesNotCloseHalfOpenCircuit(t *testing.T) {
	now := time.Now()
	st := AccountQualityState{Trips: 1, OpenUntil: now.Add(-time.Second).UnixMilli()}
	st = AdvanceAccountQualityState(st, AccountQualitySample{ID: "old-in-flight", At: now.UnixMilli()})
	require.Equal(t, 1, st.Trips)
	require.Zero(t, st.RecoverySuccesses)
}
func TestQualityLatencyDoesNotCrossModelBuckets(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	a := &Account{ID: 1, Platform: PlatformOpenAI}
	q := AccountQualityRequest{Model: "model-a", Capability: "generation", Transport: "http", Stream: true}
	svc.quality.states[AccountQualityBucket(a, q)] = cachedQualityState{qualitySamples(time.Now(), 40, 0, 5), time.Now()}
	ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model-b", Stream: true})
	ranked := svc.RankAccountQuality(ctx, "model-b", []AccountQualityCandidate{{Account: a}})
	require.Equal(t, "unknown", ranked[0].Summary.State)
	require.Nil(t, ranked[0].Summary.P50MS)
}

func TestAccountQualityCircuitRequiresThreeFailuresWithinSixtySeconds(t *testing.T) {
	now := time.Now()
	state := AccountQualityState{}
	for i := 0; i < 3; i++ {
		state = AdvanceAccountQualityState(state, AccountQualitySample{ID: fmt.Sprint(i), At: now.Add(time.Duration(i) * 50 * time.Second).UnixMilli(), Failed: true, Transient: true})
	}
	require.Zero(t, state.Trips, "three failures spread over 100 seconds must not trip the 60-second rule")
}

func TestAccountQualityRecoveryRequiresConsecutiveSuccesses(t *testing.T) {
	now := time.Now()
	state := AccountQualityState{Trips: 1, OpenUntil: now.Add(-time.Second).UnixMilli()}
	state = AdvanceAccountQualityState(state, AccountQualitySample{ID: "success1", RecoveryEpoch: 1, At: now.UnixMilli()})
	state = AdvanceAccountQualityState(state, AccountQualitySample{ID: "auth-failure", RecoveryEpoch: 1, At: now.Add(time.Second).UnixMilli(), Failed: true})
	state = AdvanceAccountQualityState(state, AccountQualitySample{ID: "success2", RecoveryEpoch: 1, At: now.Add(2 * time.Second).UnixMilli()})
	require.Equal(t, 1, state.Trips)
	require.Equal(t, 1, state.RecoverySuccesses)
}

func TestAccountQualityAccountFaultCrossesModelBoundaries(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	account := &Account{ID: 81, Platform: PlatformOpenAI}
	for i := 0; i < 3; i++ {
		ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: fmt.Sprint("model-", i)})
		_, attempt, err := svc.BeginAccountQualityAttempt(ctx, account)
		require.NoError(t, err)
		require.Error(t, attempt.Finish(nil, syscall.ECONNRESET))
	}
	ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "another-model"})
	_, _, err := svc.BeginAccountQualityAttempt(ctx, account)
	require.Error(t, err)
	ranked := svc.RankQualityAccounts(ctx, "another-model", []*Account{account}, 0)
	require.Empty(t, ranked)
}

func TestAccountQualityExplorationIsStableWithinRequestAndBounded(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	healthy := &Account{ID: 1, Platform: PlatformOpenAI}
	unknown := &Account{ID: 2, Platform: PlatformOpenAI}
	explored := 0
	for i := 0; i < 100; i++ {
		ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model"})
		svc.quality.states[AccountQualityBucket(healthy, qualityRequest(ctx, ""))] = cachedQualityState{qualitySamples(time.Now(), 40, 0, 100), time.Now()}
		first := svc.RankQualityAccounts(ctx, "model", []*Account{healthy, unknown}, 0)[0].ID
		for j := 0; j < 3; j++ {
			require.Equal(t, first, svc.RankQualityAccounts(ctx, "model", []*Account{healthy, unknown}, 0)[0].ID)
		}
		if first == unknown.ID {
			explored++
		}
	}
	require.Equal(t, 5, explored)
}

func TestAccountQualityUsesWireErrorButNeverCountsProtocolRepairTwice(t *testing.T) {
	for _, status := range []int{403, 429, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			svc := NewRateLimitService(nil, nil, nil, nil, nil)
			account := &Account{ID: 6, Platform: PlatformOpenAI}
			ctx := WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model"})
			ctx, attempt, err := svc.BeginAccountQualityAttempt(ctx, account)
			require.NoError(t, err)
			response := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream failure"}}`))}
			ObserveAccountQualityHTTPResult(ctx, response, nil)
			_, err = io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			err = attempt.Finish(nil, errors.New("adapter mapped the wire error"))
			require.Error(t, err)
			state := svc.quality.states[AccountQualityBucket(account, qualityRequest(ctx, ""))].state
			if status == 429 {
				require.Empty(t, state.Samples)
			} else {
				require.Len(t, state.Samples, 1)
				require.True(t, state.Samples[0].Failed)
			}
		})
	}
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	account := &Account{ID: 7, Platform: PlatformOpenAI}
	ctx, attempt, err := svc.BeginAccountQualityAttempt(WithAccountQualityRequest(context.Background(), AccountQualityRequest{Model: "model"}), account)
	require.NoError(t, err)
	rejected := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid field"}}`))}
	ObserveAccountQualityHTTPResult(ctx, rejected, nil)
	_, err = io.ReadAll(rejected.Body)
	require.NoError(t, err)
	ObserveAccountQualityHTTPResult(ctx, &http.Response{StatusCode: 200}, nil)
	require.NoError(t, rejected.Body.Close(), "closing a previous response must not replace a later success")
	require.NoError(t, attempt.Finish(&OpenAIForwardResult{}, nil))
	state := svc.quality.states[AccountQualityBucket(account, qualityRequest(ctx, ""))].state
	require.Len(t, state.Samples, 1)
	require.False(t, state.Samples[0].Failed)
}

func TestAccountBalanceSummaryNeverPersistsUpstreamSecrets(t *testing.T) {
	message, ok := ClassifyAccountBalanceError(403, []byte(`{"error":{"message":"Insufficient account balance; private-value-here sk-test-secret api_key=another-secret"}}`))
	require.True(t, ok)
	require.Equal(t, "Insufficient account balance", message)
}

func TestAccountQualityDispatchAllowsOnlyOneUnexecutedProtocolRepair(t *testing.T) {
	svc := NewRateLimitService(nil, nil, nil, nil, nil)
	ctx, attempt, err := svc.BeginAccountQualityAttempt(context.Background(), &Account{ID: 92, Platform: PlatformOpenAI})
	require.NoError(t, err)
	defer attempt.Abort()
	require.NoError(t, ValidateAccountQualityDispatch(ctx))
	ObserveAccountQualityHTTPResult(ctx, &http.Response{StatusCode: 400}, nil)
	require.NoError(t, ValidateAccountQualityDispatch(ctx))
	ObserveAccountQualityHTTPResult(ctx, &http.Response{StatusCode: 400}, nil)
	require.Error(t, ValidateAccountQualityDispatch(ctx), "a third actual inference dispatch is forbidden")
}
