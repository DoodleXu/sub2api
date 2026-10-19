package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type accountQualityAttempt struct {
	owner                *RateLimitService
	account              *Account
	req                  AccountQualityRequest
	key                  string
	id                   string
	start                time.Time
	ctx                  context.Context
	done                 atomic.Bool
	balance              atomic.Bool
	sent                 atomic.Bool
	repairs              atomic.Int32
	wireRepairs          atomic.Int32
	creditTransitions    atomic.Int32
	credentialAccount    *Account
	wireOutcome          atomic.Pointer[accountQualityWireOutcome]
	recoveryEpoch        int
	accountRecoveryEpoch int
	accountKey           string
	stop                 chan struct{}
}

func (s *RateLimitService) BeginAccountQualityAttempt(ctx context.Context, a *Account) (context.Context, *accountQualityAttempt, error) {
	ctx = withAccountQualityCredentials(ctx)
	a = accountQualityCredentialSnapshot(ctx, a)
	if a == nil {
		return ctx, nil, nil
	}
	if active, ok := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt); ok && active != nil && !active.done.Load() && active.account.ID == a.ID {
		return ctx, nil, nil
	}
	if s == nil || a == nil {
		return ctx, nil, nil
	}
	if err := s.ValidateAccountDispatch(ctx, a); err != nil {
		return ctx, nil, err
	}
	if s.quality == nil {
		return ctx, nil, nil
	}
	var credentialAccount *Account
	if _, safeRepo := s.accountRepo.(AccountBalancePauseRepository); safeRepo && a.ParentAccountID != nil {
		parent, err := s.accountRepo.GetByID(ctx, *a.ParentAccountID)
		if err != nil {
			return ctx, nil, err
		}
		credentialAccount = parent
	}
	req := qualityRequest(ctx, "")
	key := AccountQualityBucket(a, req)
	id := uuid.NewString()
	accountKey := accountQualityAccountBucket(a)
	states := s.quality.snapshots(ctx, []string{key, accountKey})
	state := states[key]
	accountState := states[accountKey]
	summary := mergeAccountQualitySummary(SummarizeAccountQuality(state, time.Now()), SummarizeAccountQuality(accountState, time.Now()))
	if summary.State == "open" {
		return ctx, nil, &UpstreamFailoverError{StatusCode: 503, Reason: "quality_circuit_open"}
	}
	probe := summary.State == "unknown" || summary.State == "half_open"
	accountProbe := accountState.Trips > 0
	if s.quality.store != nil {
		leaseCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		ok, err := s.quality.store.AcquireAccountQualityPermit(leaseCtx, key, id, probe)
		cancel()
		if err != nil {
			return ctx, nil, &UpstreamFailoverError{StatusCode: 503, Reason: "quality_probe_unavailable"}
		}
		if err == nil && !ok {
			return ctx, nil, &UpstreamFailoverError{StatusCode: 503, Reason: "quality_circuit_open"}
		}
	} else if probe {
		s.quality.mu.Lock()
		until := s.quality.leases[key]
		if time.Now().Before(until) || (accountProbe && time.Now().Before(s.quality.leases[accountKey])) {
			s.quality.mu.Unlock()
			return ctx, nil, &UpstreamFailoverError{StatusCode: 503, Reason: "quality_probe_busy"}
		}
		s.quality.leases[key] = time.Now().Add(15 * time.Minute)
		if accountProbe {
			s.quality.leases[accountKey] = s.quality.leases[key]
		}
		s.quality.mu.Unlock()
	}
	attempt := &accountQualityAttempt{owner: s, account: a, req: req, key: key, id: id, start: time.Now(), ctx: ctx, recoveryEpoch: state.Trips, accountRecoveryEpoch: accountState.Trips, accountKey: accountKey, stop: make(chan struct{}), credentialAccount: credentialAccount}
	attemptCtx := context.WithValue(ctx, qualityAttemptKey{}, attempt)
	attempt.ctx = attemptCtx
	go attempt.renewLease()
	return attemptCtx, attempt, nil
}

type accountQualityLeaseStore interface {
	RenewAccountQualityPermit(context.Context, string, string) error
}

func (a *accountQualityAttempt) renewLease() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			if store, ok := a.owner.quality.store.(accountQualityLeaseStore); ok {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = store.RenewAccountQualityPermit(ctx, a.key, a.id)
				_ = store.RenewAccountQualityPermit(ctx, a.accountKey, a.id)
				cancel()
			} else if a.owner.quality.store == nil {
				q := a.owner.quality
				q.mu.Lock()
				for _, key := range []string{a.key, a.accountKey} {
					if _, ok := q.leases[key]; ok {
						q.leases[key] = time.Now().Add(15 * time.Minute)
					}
				}
				q.mu.Unlock()
			}
		}
	}
}

func (a *accountQualityAttempt) Finish(result any, err error) error {
	if a == nil || a.done.Swap(true) {
		return err
	}
	if a.stop != nil {
		close(a.stop)
	}
	q := a.owner.quality
	defer func() {
		if q.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			_ = q.store.ReleaseAccountQualityPermit(ctx, a.key, a.id)
			_ = q.store.ReleaseAccountQualityPermit(ctx, a.accountKey, a.id)
		}
		q.mu.Lock()
		delete(q.leases, a.key)
		delete(q.leases, a.accountKey)
		q.mu.Unlock()
	}()
	if a.balance.Load() {
		err = &UpstreamFailoverError{StatusCode: 403, ResponseBody: []byte(`{"error":{"message":"Insufficient account balance"}}`), Reason: "insufficient_account_balance", NextAccountAction: NextAccountRetry}
	}
	sample := AccountQualitySample{RecoveryEpoch: a.recoveryEpoch, ID: a.id, At: time.Now().UnixMilli(), DurationMS: time.Since(a.start).Milliseconds()}
	model := ""
	var ttft *int
	reportErr := err
	if wire := a.wireOutcome.Load(); wire != nil && wire.err != nil {
		reportErr = accountQualityObservationError(err, wire.err)
	}
	if result == nil && reportErr == nil && a.req.Capability == "token_count" && !a.sent.Load() {
		return err
	}
	success := reportErr == nil
	switch r := result.(type) {
	case *ForwardResult:
		if r != nil {
			model = r.UpstreamModel
			ttft = r.FirstTokenMs
			if r.Duration > 0 {
				sample.DurationMS = r.Duration.Milliseconds()
			}
		}
	case *SystemOneForwardResult:
		if r != nil {
			model = r.UpstreamModel
			ttft = r.FirstTokenMs
			sample.DurationMS = r.Duration.Milliseconds()
		}
	case *OpenAIForwardResult:
		if r != nil {
			model = r.UpstreamModel
			ttft = r.FirstTokenMs
			success = success && r.SucceededForScheduling()
			if r.Duration > 0 {
				sample.DurationMS = r.Duration.Milliseconds()
			}
		}
	}
	if success {
		if a.req.Stream && a.req.Capability == "generation" {
			if ttft != nil && *ttft > 0 {
				sample.LatencyMS = int64(*ttft)
			}
		} else {
			sample.LatencyMS = sample.DurationMS
		}
	} else {
		sample.Failed = true
		var failure *UpstreamFailoverError
		if errors.As(err, &failure) {
			if a.owner.HandleAccountBalanceError(a.ctx, a.account, failure.StatusCode, failure.ResponseBody) {
				failure.RetryableOnSameAccount = false
				failure.NextAccountAction = NextAccountRetry
			}
		}
		if reportErr == nil {
			reportErr = &UpstreamFailoverError{StatusCode: 502, Reason: "missing_success_terminal"}
		}
		eligible, transient := classifyAccountQualityFailure(reportErr)
		if !eligible {
			return err
		}
		sample.Transient = transient
	}
	if a.ctx.Err() != nil {
		return err
	}
	observationAccount := accountQualityCredentialSnapshot(a.ctx, a.account)
	key := AccountQualityBucket(observationAccount, a.req)
	accountKey := accountQualityAccountBucket(observationAccount)
	if AccountSchedulingGeneration(observationAccount) != AccountSchedulingGeneration(a.account) {
		// The issued token belongs to a new generation. Its outcome must not
		// close an old generation's recovery circuit or disappear with it.
		sample.RecoveryEpoch = 0
	}
	if model != "" {
		req := a.req
		req.Model = model
		copyAccount := *observationAccount
		copyAccount.Credentials = nil
		key = AccountQualityBucket(&copyAccount, req)
	}
	sample.Dimension = a.req
	sample.Dimension.ID = ""
	if model != "" {
		sample.Dimension.Model = model
	}
	q.observe(a.ctx, key, sample)
	if success || accountWideQualityFailure(reportErr) {
		sample.RecoveryEpoch = a.accountRecoveryEpoch
		if accountKey != a.accountKey {
			sample.RecoveryEpoch = 0
		}
		sample.Dimension = AccountQualityRequest{Capability: "account"}
		sample.LatencyMS = 0
		q.observe(a.ctx, accountKey, sample)
	}
	return err
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
	attempt.sent.Store(true)
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

func accountWideQualityFailure(err error) bool {
	var failure *UpstreamFailoverError
	if errors.As(err, &failure) {
		if failure.Scope == GatewayFailureScopeRequest || failure.Scope == GatewayFailureScopeProvider || failure.RequestScopedTransient {
			return false
		}
		eligible, _ := classifyAccountQualityFailure(err)
		return eligible && (failure.StatusCode == 401 || failure.StatusCode == 403)
	}
	eligible, _ := classifyAccountQualityFailure(err)
	return eligible // Typed connection/timeout errors apply to the whole account.
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
	if wire != nil && wire.err != nil {
		var failure *UpstreamFailoverError
		repairable := errors.As(wire.err, &failure) && (failure.StatusCode == 400 || failure.StatusCode == 401 || failure.StatusCode == 404 || failure.StatusCode == 405 || failure.StatusCode == 422)
		if !repairable || attempt.wireRepairs.Add(1) > 1 {
			return &UpstreamFailoverError{StatusCode: 503, Reason: "quality_repair_exhausted"}
		}
	}
	return nil
}

func classifyAccountQualityFailure(err error) (bool, bool) {
	if err == nil {
		return false, false
	}
	if errors.Is(err, context.Canceled) {
		return false, false
	}
	var fail *UpstreamFailoverError
	if errors.As(err, &fail) {
		if fail.Scope == GatewayFailureScopeRequest || fail.Scope == GatewayFailureScopeProvider || fail.RequestScopedTransient {
			return false, false
		}
		if strings.HasPrefix(string(fail.Reason), "quality_") || fail.Reason == "scheduling_state_changed" {
			return false, false
		}
		if _, ok := ClassifyAccountBalanceError(fail.StatusCode, fail.ResponseBody); ok {
			return true, false
		}
		category := ClassifyChannelMonitorV2Error(ChannelMonitorV2ErrorInput{StatusCode: fail.StatusCode, Message: extractUpstreamErrorMessage(fail.ResponseBody)})
		switch category {
		case "content_policy", "context_limit", "invalid_request", "model_unsupported", "group_access", "client_cancelled":
			return false, false
		}
		if fail.StatusCode == 429 || fail.StatusCode == 400 || fail.StatusCode == 404 || fail.StatusCode == 413 {
			return false, false
		}
		if fail.StatusCode == 401 || fail.StatusCode == 403 {
			return true, false
		}
		return fail.StatusCode >= 500, fail.StatusCode >= 500
	}
	var img *OpenAIImagesUpstreamError
	if errors.As(err, &img) {
		return img.StatusCode >= 500, img.StatusCode >= 500
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
		return true, true
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return true, true
	}
	// Only classified upstream failures affect health; application/validation
	// errors must not silently become an account penalty.
	return false, false
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

func (a *accountQualityAttempt) Abort() {
	if a == nil || a.done.Swap(true) {
		return
	}
	if a.stop != nil {
		close(a.stop)
	}
	q := a.owner.quality
	if q.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_ = q.store.ReleaseAccountQualityPermit(ctx, a.key, a.id)
		_ = q.store.ReleaseAccountQualityPermit(ctx, a.accountKey, a.id)
	}
	q.mu.Lock()
	delete(q.leases, a.key)
	delete(q.leases, a.accountKey)
	q.mu.Unlock()
}

func (s *RateLimitService) ValidateAccountDispatch(ctx context.Context, a *Account) error {
	if s == nil || a == nil {
		return nil
	}
	a = accountQualityCredentialSnapshot(ctx, a)
	if _, ok := s.accountRepo.(AccountBalancePauseRepository); ok {
		current, err := s.accountRepo.GetByID(ctx, a.ID)
		if err != nil {
			return err
		}
		if current == nil || !current.IsSchedulable() || current.Extra[AccountBalancePauseKey] != nil || s.balancePauseInFlight(current) || AccountSchedulingGeneration(current) != AccountSchedulingGeneration(a) {
			return &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeAccount, Reason: "scheduling_state_changed"}
		}
	}
	if _, safeRepo := s.accountRepo.(AccountBalancePauseRepository); safeRepo && a.ParentAccountID != nil {
		parent, err := s.accountRepo.GetByID(ctx, *a.ParentAccountID)
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
