package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// This compatibility boundary now owns only financial and credential safety.
// The upstream scheduler owns ranking, health feedback and retry decisions.
type accountQualityAttempt struct {
	owner             *RateLimitService
	account           *Account
	ctx               context.Context
	done              atomic.Bool
	balance           atomic.Bool
	repairs           atomic.Int32
	creditTransitions atomic.Int32
	credentialAccount *Account
	wireOutcome       atomic.Pointer[accountQualityWireOutcome]
}

func (s *RateLimitService) BeginAccountQualityAttempt(ctx context.Context, a *Account) (context.Context, *accountQualityAttempt, error) {
	ctx = withAccountQualityCredentials(ctx)
	a = accountQualityCredentialSnapshot(ctx, a)
	if s == nil || a == nil {
		return ctx, nil, nil
	}
	if active, ok := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt); ok && active != nil && !active.done.Load() && active.account.ID == a.ID {
		return ctx, nil, nil
	}
	if err := s.ValidateAccountDispatch(ctx, a); err != nil {
		return ctx, nil, err
	}
	var owner *Account
	if _, safeRepo := s.accountRepo.(AccountBalancePauseRepository); safeRepo && a.ParentAccountID != nil {
		var err error
		owner, err = s.accountDispatchState(ctx, *a.ParentAccountID)
		if err != nil {
			return ctx, nil, err
		}
	}
	attempt := &accountQualityAttempt{owner: s, account: a, credentialAccount: owner}
	ctx = context.WithValue(ctx, qualityAttemptKey{}, attempt)
	attempt.ctx = ctx
	return ctx, attempt, nil
}

func (a *accountQualityAttempt) Finish(_ any, err error) error {
	if a == nil || a.done.Swap(true) {
		return err
	}
	reportErr := err
	if wire := a.wireOutcome.Load(); wire != nil && wire.err != nil {
		reportErr = accountQualityObservationError(err, wire.err)
	}
	var failure *UpstreamFailoverError
	if errors.As(reportErr, &failure) {
		a.owner.HandleAccountBalanceError(a.ctx, a.account, failure.StatusCode, failure.ResponseBody)
	}
	if a.balance.Load() {
		return &UpstreamFailoverError{StatusCode: 403, ResponseBody: []byte(`{"error":{"message":"Insufficient account balance"}}`), Reason: "insufficient_account_balance", NextAccountAction: NextAccountRetry}
	}
	return err
}

func (a *accountQualityAttempt) Abort() {
	if a != nil {
		a.done.Store(true)
	}
}

type accountQualityWireOutcome struct{ err error }

// Adapters know whether a failure belongs to the request or provider. Keep
// that classification even when the transport has a more complete error body.
func accountQualityObservationError(classified, wire error) error {
	var failure *UpstreamFailoverError
	if !errors.As(classified, &failure) {
		return wire
	}
	copy := *failure
	var raw *UpstreamFailoverError
	if errors.As(wire, &raw) {
		if copy.StatusCode == 0 {
			copy.StatusCode = raw.StatusCode
		}
		if len(copy.ResponseBody) == 0 {
			copy.ResponseBody = raw.ResponseBody
		}
	}
	return &copy
}

// ObserveAccountQualityHTTPResult keeps the original transport failure and a
// bounded error body even when an adapter maps it to a generic client error.
func ObserveAccountQualityHTTPResult(ctx context.Context, response *http.Response, err error) {
	attempt, _ := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt)
	if attempt == nil {
		return
	}
	outcome := &accountQualityWireOutcome{err: err}
	if err == nil && response != nil && response.StatusCode >= 400 {
		outcome.err = &UpstreamFailoverError{StatusCode: response.StatusCode}
		if response.Body != nil {
			response.Body = &accountQualityErrorBody{ReadCloser: response.Body, attempt: attempt, status: response.StatusCode, outcome: outcome}
		}
	}
	attempt.wireOutcome.Store(outcome)
}

type accountQualityErrorBody struct {
	io.ReadCloser
	attempt *accountQualityAttempt
	status  int
	outcome *accountQualityWireOutcome
	body    []byte
}

func (b *accountQualityErrorBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if remaining := 16384 - len(b.body); remaining > 0 {
		b.body = append(b.body, p[:min(n, remaining)]...)
	}
	return n, err
}

func (b *accountQualityErrorBody) Close() error {
	b.attempt.wireOutcome.CompareAndSwap(b.outcome, &accountQualityWireOutcome{err: &UpstreamFailoverError{StatusCode: b.status, ResponseBody: append([]byte(nil), b.body...)}})
	return b.ReadCloser.Close()
}

// ValidateAccountQualityDispatch is called by the common transport immediately
// before sending bytes, including protocol repairs after a token refresh.
const accountQualityGinContextKey = "account_quality_attempt_context"

func bindAccountQualityAttemptContext(c *gin.Context, ctx context.Context) {
	if c != nil {
		c.Set(accountQualityGinContextKey, ctx)
	}
}

// AllowAccountProtocolRepair is the one repair budget shared by nested adapters
// for one account attempt. It never permits repair after a financial failure.
func AllowAccountProtocolRepair(ctx context.Context) bool {
	attempt, _ := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt)
	if attempt == nil {
		return true
	}
	return !attempt.balance.Load() && attempt.repairs.Add(1) == 1
}

func ValidateAccountQualityDispatch(ctx context.Context) error {
	attempt, _ := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt)
	if attempt == nil {
		return nil
	}
	if attempt.balance.Load() {
		return &UpstreamFailoverError{StatusCode: 403, Reason: "insufficient_account_balance", NextAccountAction: NextAccountRetry}
	}
	if err := attempt.owner.ValidateAccountDispatch(ctx, attempt.account); err != nil {
		return err
	}
	wire := attempt.wireOutcome.Load()
	if transition, _ := ctx.Value(accountQualityCreditsTransitionKey{}).(bool); transition {
		var failure *UpstreamFailoverError
		if wire != nil && !attempt.balance.Load() && errors.As(wire.err, &failure) && failure.StatusCode == http.StatusTooManyRequests && attempt.creditTransitions.Add(1) == 1 {
			return nil
		}
		return &UpstreamFailoverError{StatusCode: 503, Reason: "quality_repair_exhausted"}
	}
	// Each adapter decides whether a pre-output retry is safe and bounded.
	// Do not override provider-scoped capacity retries with a second global gate.

	return nil
}

func AccountQualityCapability(path string) string {
	switch {
	case strings.Contains(path, "count_tokens") || strings.Contains(path, "input_tokens"):
		return "token_count"
	case strings.Contains(path, "images"):
		return "image"
	case strings.Contains(path, "videos"):
		return "video"
	case strings.Contains(path, "audio"):
		return "audio"
	case strings.Contains(path, "live"):
		return "live"
	case strings.Contains(path, "embeddings"):
		return "embedding"
	case strings.Contains(path, "search"):
		return "search"
	default:
		return "generation"
	}
}

func (s *RateLimitService) ValidateAccountDispatch(ctx context.Context, a *Account) error {
	if s == nil || a == nil {
		return nil
	}
	a = accountQualityCredentialSnapshot(ctx, a)
	if _, ok := s.accountRepo.(AccountBalancePauseRepository); ok {
		current, err := s.accountDispatchState(ctx, a.ID)
		if err != nil {
			return err
		}
		if current == nil || !current.IsSchedulable() || current.Extra[AccountBalancePauseKey] != nil || s.balancePauseInFlight(current) || AccountSchedulingGeneration(current) != AccountSchedulingGeneration(a) {
			return &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeAccount, Reason: "scheduling_state_changed"}
		}
	}
	if _, safeRepo := s.accountRepo.(AccountBalancePauseRepository); safeRepo && a.ParentAccountID != nil {
		parent, err := s.accountDispatchState(ctx, *a.ParentAccountID)
		if err != nil {
			return err
		}
		if parent == nil || !parent.IsCredentialUsableForShadow() || parent.Extra[AccountBalancePauseKey] != nil || s.balancePauseInFlight(parent) {
			return &UpstreamFailoverError{StatusCode: 503, Reason: "scheduling_state_changed"}
		}
		if active, _ := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt); active != nil && active.credentialAccount != nil && AccountSchedulingGeneration(parent) != AccountSchedulingGeneration(accountQualityCredentialSnapshot(ctx, active.credentialAccount)) {
			return &UpstreamFailoverError{StatusCode: 503, Reason: "scheduling_state_changed"}
		}
		if err := s.validateAccountBalanceFence(ctx, parent); err != nil {
			return err
		}
	}
	return s.validateAccountBalanceFence(ctx, a)
}

func (s *RateLimitService) validateAccountBalanceFence(ctx context.Context, a *Account) error {

	if store, ok := s.tempUnschedCache.(interface {
		HasAccountBalanceBlock(context.Context, int64, int64) (bool, error)
	}); ok {
		readCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		blocked, err := store.HasAccountBalanceBlock(readCtx, a.ID, AccountSchedulingGeneration(a))
		cancel()
		if err != nil {
			return &UpstreamFailoverError{StatusCode: 503, Reason: "quality_balance_block_unavailable"}
		}
		if blocked {
			return &UpstreamFailoverError{StatusCode: 503, Reason: "scheduling_state_changed"}
		}
	}

	return nil
}

// AccountDispatchStateRepository avoids loading groups/proxies for a safety check.
// The returned account is a read-only scheduling/credential projection.
type AccountDispatchStateRepository interface {
	GetAccountDispatchState(context.Context, int64) (*Account, error)
}

func (s *RateLimitService) accountDispatchState(ctx context.Context, id int64) (*Account, error) {
	if repo, ok := s.accountRepo.(AccountDispatchStateRepository); ok {
		return repo.GetAccountDispatchState(ctx, id)
	}
	return s.accountRepo.GetByID(ctx, id)
}
