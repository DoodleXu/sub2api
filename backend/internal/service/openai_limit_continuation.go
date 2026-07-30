package service

import (
	"context"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAIContinueSchedulingAfterLimitExtraKey controls whether an OpenAI account
// remains eligible after quota/rate-limit signals. Persistent account health
// failures (manual pause, auth errors, expiry, overload and temp quarantine)
// remain authoritative.
const OpenAIContinueSchedulingAfterLimitExtraKey = "openai_continue_scheduling_after_limit"

type openAILimitContinuationSelectionMode uint8

type openAIAccountSelectionPool struct {
	accounts []Account
}

const (
	openAILimitContinuationSelectionNormal openAILimitContinuationSelectionMode = iota
	openAILimitContinuationSelectionOnly
	openAILimitContinuationSelectionExclude
)

func openAIAccountMatchesLimitContinuationSelection(account *Account, mode openAILimitContinuationSelectionMode) bool {
	enabled := account != nil && account.IsOpenAIContinueSchedulingAfterLimitEnabled()
	switch mode {
	case openAILimitContinuationSelectionOnly:
		return enabled
	case openAILimitContinuationSelectionExclude:
		return !enabled
	default:
		return true
	}
}

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

func partitionOpenAILimitContinuationAccounts(accounts []Account) (priority, normal []Account) {
	priority = make([]Account, 0, len(accounts))
	normal = make([]Account, 0, len(accounts))
	for i := range accounts {
		if accounts[i].IsOpenAIContinueSchedulingAfterLimitEnabled() {
			priority = append(priority, accounts[i])
			continue
		}
		normal = append(normal, accounts[i])
	}
	return priority, normal
}

func openAIAccountLimitContinuationAllowed(ctx context.Context, account *Account) bool {
	_ = ctx
	return account != nil && account.IsOpenAIContinueSchedulingAfterLimitEnabled()
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
