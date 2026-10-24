package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	sameAccountRetryDelay      = 500 * time.Millisecond
	maxRequestScopedRetryDelay = 8 * time.Second
)

func sameAccountRetryDelayFor(e *service.UpstreamFailoverError, count int) time.Duration {
	delay := sameAccountRetryDelay
	if e == nil {
		return delay
	}
	if e.SameAccountRetryDelay > 0 {
		delay = e.SameAccountRetryDelay
	} else if e.RequestScopedTransient {
		for i := 1; i < count && delay < maxRequestScopedRetryDelay; i++ {
			delay *= 2
		}
		if delay > maxRequestScopedRetryDelay {
			delay = maxRequestScopedRetryDelay
		}
	}
	// Retry-After is a lower bound. Long hints cause failover in the caller,
	// never an earlier same-account retry or an unbounded sleep.
	raw := strings.TrimSpace(e.ResponseHeaders.Get("Retry-After"))
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
		if seconds > maxRequestScopedRetryDelay.Seconds() {
			return maxRequestScopedRetryDelay + time.Second
		}
		if floor := time.Duration(seconds * float64(time.Second)); floor > delay {
			delay = floor
		}
	} else if when, err := http.ParseTime(raw); err == nil {
		if floor := time.Until(when); floor > delay {
			delay = floor
		}
	}
	return delay
}

// effectiveSameAccountRetryLimit applies an error-specific cap without
// overriding an explicit account setting of zero (which disables retries).
func effectiveSameAccountRetryLimit(failoverErr *service.UpstreamFailoverError, account *service.Account) int {
	if account == nil {
		return 0
	}
	limit := account.GetPoolModeRetryCount()
	if limit > 0 && failoverErr != nil && failoverErr.SameAccountRetryMax > 0 && failoverErr.SameAccountRetryMax < limit {
		return failoverErr.SameAccountRetryMax
	}
	return limit
}

// Only adapters that confirmed a pre-output, replayable transient failure can
// opt in. Debt, credentials, zero retry budgets and expired deadlines never do.
func sameAccountRetryAllowed(e *service.UpstreamFailoverError, count, limit int) bool {
	if e == nil || !e.RetryableOnSameAccount || !e.ShouldRetryNextAccount() || limit <= 0 {
		return false
	}
	if e.IsCredentialFailure() || e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden || e.Reason == "scheduling_state_changed" || e.Reason == "insufficient_account_balance" {
		return false
	}
	if _, debt := service.ClassifyAccountBalanceError(e.StatusCode, e.ResponseBody); debt {
		return false
	}
	if e.SameAccountRetryMax > 0 && e.SameAccountRetryMax < limit {
		limit = e.SameAccountRetryMax
	}
	if count >= limit {
		return false
	}
	delay := sameAccountRetryDelayFor(e, count+1)
	// A longer Retry-After is a signal to fail over, never to retry earlier.
	if delay > maxRequestScopedRetryDelay {
		return false
	}
	return e.SameAccountRetryDeadline.IsZero() || time.Now().Add(delay).Before(e.SameAccountRetryDeadline)
}

// retrySafeAccount is called only after the endpoint's no-output/replay check.
func retrySafeAccount(ctx context.Context, counts map[int64]int, account *service.Account, failure *service.UpstreamFailoverError) (retry, canceled bool) {
	if account == nil || !sameAccountRetryAllowed(failure, counts[account.ID], effectiveSameAccountRetryLimit(failure, account)) {
		return false, false
	}
	counts[account.ID]++
	if !sleepWithContext(ctx, sameAccountRetryDelayFor(failure, counts[account.ID])) {
		return false, true
	}
	return true, false
}
