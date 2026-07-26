package service

import (
	"context"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAIContinueSchedulingAfterLimitExtraKey controls whether an OpenAI account
// remains eligible after quota/rate-limit signals. Persistent account health
// failures (manual pause, auth errors, expiry, overload and temp quarantine)
// remain authoritative.
const OpenAIContinueSchedulingAfterLimitExtraKey = "openai_continue_scheduling_after_limit"

type openAIAccountScheduleSessionStartContextKey struct{}

// IsOpenAIContinueSchedulingAfterLimitEnabled reports whether this OpenAI
// account opted into best-effort scheduling after quota exhaustion or 429s.
func (a *Account) IsOpenAIContinueSchedulingAfterLimitEnabled() bool {
	return a != nil && a.IsOpenAI() && resolveAccountExtraBool(a.Extra, OpenAIContinueSchedulingAfterLimitExtraKey)
}

// IsSchedulableIgnoringRateLimit keeps every non-rate-limit health gate intact.
func (a *Account) IsSchedulableIgnoringRateLimit() bool {
	if a == nil || a.IsArchived() || !a.IsActive() || !a.Schedulable {
		return false
	}
	now := time.Now()
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return false
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		return false
	}
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) && !isOpenAI429TempUnschedulable(a) {
		return false
	}
	return true
}

func isOpenAI429TempUnschedulable(account *Account) bool {
	if account == nil || !account.IsOpenAIContinueSchedulingAfterLimitEnabled() {
		return false
	}
	return gjson.Get(account.TempUnschedulableReason, "status_code").Int() == 429
}

// WithOpenAIAccountScheduleSessionStart freezes whether the current inbound
// request starts a new conversation. The value must remain stable across all
// failover attempts because the first selection may create a sticky binding.
func WithOpenAIAccountScheduleSessionStart(ctx context.Context, isStart bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIAccountScheduleSessionStartContextKey{}, isStart)
}

func openAIAccountScheduleSessionStart(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	isStart, _ := ctx.Value(openAIAccountScheduleSessionStartContextKey{}).(bool)
	return isStart
}

// WithOpenAIAccountScheduleSessionContext classifies a request once, before
// the first account selection. A previous_response_id, an existing sticky
// binding, or explicit multi-turn/tool history proves a continuation.
func (s *OpenAIGatewayService) WithOpenAIAccountScheduleSessionContext(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestBody ...[]byte,
) context.Context {
	if strings.TrimSpace(previousResponseID) != "" {
		return WithOpenAIAccountScheduleSessionStart(ctx, false)
	}
	isStart := true
	if strings.TrimSpace(sessionHash) != "" && s != nil && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			isStart = false
		}
	}
	if isStart && len(requestBody) > 0 && openAIRequestBodyProvesContinuation(requestBody[0]) {
		isStart = false
	}
	return WithOpenAIAccountScheduleSessionStart(ctx, isStart)
}

func openAIRequestBodyProvesContinuation(body []byte) bool {
	for _, path := range []string{"messages", "input"} {
		items := gjson.GetBytes(body, path)
		if !items.Exists() || !items.IsArray() {
			continue
		}
		userMessages := 0
		continuation := false
		items.ForEach(func(_, item gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
			typeName := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			switch role {
			case "assistant", "tool":
				continuation = true
			case "user":
				userMessages++
				continuation = userMessages > 1
			}
			if typeName == "function_call_output" || typeName == "tool_result" {
				continuation = true
			}
			return !continuation
		})
		if continuation {
			return true
		}
	}
	return false
}

func openAIAccountLimitContinuationAllowed(ctx context.Context, account *Account) bool {
	_ = ctx
	return account != nil && account.IsOpenAIContinueSchedulingAfterLimitEnabled()
}

// openAIAccountReachedRollingLimit is intentionally stricter than the
// configurable auto-pause threshold: a new session is rejected only after the
// real 5h or 7d window reaches 100%.
func openAIAccountReachedRollingLimit(account *Account, now time.Time) (bool, string) {
	if account == nil || !account.IsOpenAIContinueSchedulingAfterLimitEnabled() {
		return false, ""
	}
	if readOpenAIQuotaUsedPercent(account.Extra, "5h") >= 100 && !openAIQuotaWindowReset(account.Extra, "5h", now) {
		return true, "5h"
	}
	if readOpenAIQuotaUsedPercent(account.Extra, "7d") >= 100 && !openAIQuotaWindowReset(account.Extra, "7d", now) {
		return true, "7d"
	}
	return false, ""
}

// PrepareOpenAILimitContinuationFailover makes provider/account failures from
// an opted-in account retry another account and prevents this account's raw
// upstream error from becoming the exhausted-failover client response.
func PrepareOpenAILimitContinuationFailover(account *Account, failoverErr *UpstreamFailoverError) bool {
	if account == nil || failoverErr == nil || !account.IsOpenAIContinueSchedulingAfterLimitEnabled() {
		return false
	}
	if failoverErr.Scope == GatewayFailureScopeRequest || failoverErr.IsOpenAIRequestBodyTooLarge() {
		return false
	}
	failoverErr.NextAccountAction = NextAccountRetry
	failoverErr.RetryableOnSameAccount = false
	failoverErr.SuppressClientError = true
	return true
}
