package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const AccountBalancePauseKey = "scheduler_balance_pause"
const AccountSchedulingGenerationKey = "scheduler_generation"

// AccountBalancePauseRepository is atomic: a late failure must not pause a
// credential or scheduling generation that an administrator has replaced.
type AccountBalancePauseRepository interface {
	PauseAccountForInsufficientBalance(context.Context, *Account, AccountBalancePause) (bool, error)
}

type AccountBalancePause struct {
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	RequestID string    `json:"request_id,omitempty"`
	At        time.Time `json:"at"`
	Note      string    `json:"note"`
}

func AccountSchedulingGeneration(a *Account) int64 {
	if a == nil {
		return 0
	}
	switch n := a.Extra[AccountSchedulingGenerationKey].(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		v, _ := n.Int64()
		return v
	}
	n, _ := strconv.ParseInt(fmt.Sprint(a.Extra[AccountSchedulingGenerationKey]), 10, 64)
	return n
}

// SanitizeAccountSchedulingExtra removes retired and server-owned input. The
// database guard preserves the current pause and generation on updates.
func SanitizeAccountSchedulingExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		switch k {
		case "openai_continue_scheduling_after_limit", AccountBalancePauseKey, AccountSchedulingGenerationKey, "scheduler_manual_resume":
			continue
		}
		out[k] = v
	}
	return out
}

var accountBalanceMessage = regexp.MustCompile(`(?i)^(?:sorry[,!]?\s*)?(?:(?:your\s+)?(?:account\s+)?(?:credit\s+)?balance (?:is )?(?:insufficient|exhausted|too low)|insufficient (?:(?:account|credit) )?balance|(?:your )?credit balance is too low)(?:[\s.!,:;（(]|$)`)

// Deliberately do not search arbitrary JSON or echoed request text. Generic
// quota errors, subscription windows, and model wallets are not account debt.
func ClassifyAccountBalanceError(status int, body []byte) (string, bool) {
	if status != 200 && (status < 400 || status > 599) {
		return "", false
	}
	message, code := "", ""
	if gjson.ValidBytes(body) {
		if status == 200 && !gjson.GetBytes(body, "error").IsObject() && !gjson.GetBytes(body, "response.error").IsObject() {
			return "", false
		}
		for _, path := range []string{"error.scope", "error.param", "error.type", "response.error.scope", "detail.scope"} {
			scope := strings.ToLower(gjson.GetBytes(body, path).String())
			if strings.Contains(scope, "model") || strings.Contains(scope, "wallet") {
				return "", false
			}
		}
		for _, path := range []string{"error.message", "response.error.message", "detail.message", "message"} {
			if v := gjson.GetBytes(body, path); v.Type == gjson.String && v.String() != "" {
				message = v.String()
				break
			}
		}
		for _, path := range []string{"error.code", "response.error.code", "detail.code", "code"} {
			if v := gjson.GetBytes(body, path); v.Type == gjson.String {
				code = strings.ToLower(v.String())
				break
			}
		}
	} else {
		if status == 200 {
			return "", false
		}
		message = strings.TrimSpace(string(body))
	}
	lower := strings.ToLower(message)
	for _, scope := range []string{"upstream image account", "model quota", "per-model", "model balance", "image credits", "wallet", "quota will reset", "usage limit"} {
		if strings.Contains(lower, scope) {
			return "", false
		}
	}
	explicitCode := code == "insufficient_balance" || code == "account_balance_insufficient" || code == "credit_balance_exhausted"
	explicitMessage := accountBalanceMessage.MatchString(message) || strings.HasPrefix(message, "账户余额不足") || strings.HasPrefix(message, "账号余额不足") || strings.HasPrefix(message, "余额不足")
	if !explicitCode && !explicitMessage {
		return "", false
	}
	// Persist a canonical error summary, never arbitrary upstream prose, which
	// may contain credentials, user input, or secrets after the billing phrase.
	message = "Insufficient account balance"
	return message, true
}

type balancePausePending struct {
	generation int64
	at         time.Time
}

func (s *RateLimitService) HandleAccountBalanceError(ctx context.Context, account *Account, status int, body []byte) bool {
	if s == nil || account == nil {
		return false
	}
	message, ok := ClassifyAccountBalanceError(status, body)
	if !ok {
		return false
	}
	if active, ok := ctx.Value(qualityAttemptKey{}).(*accountQualityAttempt); ok && active != nil {
		active.balance.Store(true)
		if active.credentialAccount != nil {
			account = active.credentialAccount
		}
	}
	account = accountQualityCredentialSnapshot(ctx, account)
	now := time.Now().UTC()
	requestID := AccountQualityRequestID(ctx)
	pause := AccountBalancePause{Reason: "insufficient_account_balance", Message: message, RequestID: requestID, At: now}
	pause.Note = fmt.Sprintf("[自动停调·余额不足] %s %s 请求=%s；充值后请手动开启调度", now.Format(time.RFC3339), message, requestID)
	key := fmt.Sprintf("%d:%d", account.ID, AccountSchedulingGeneration(account))
	if _, loaded := s.balancePausePending.LoadOrStore(key, balancePausePending{AccountSchedulingGeneration(account), now}); loaded {
		return true
	}
	if store, ok := s.tempUnschedCache.(interface {
		SetAccountBalanceBlock(context.Context, int64, int64) error
	}); ok {
		blockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		if err := store.SetAccountBalanceBlock(blockCtx, account.ID, AccountSchedulingGeneration(account)); err != nil {
			slog.Error("account_balance_shared_block_failed", "account_id", account.ID, "error", err)
		}
		cancel()
	}
	persist := func() (bool, error) {
		repo, ok := s.accountRepo.(AccountBalancePauseRepository)
		if !ok {
			return false, fmt.Errorf("account balance safety repository unavailable")
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		applied, err := repo.PauseAccountForInsufficientBalance(persistCtx, account, pause)
		if err == nil {
			s.balancePausePending.Delete(key)
			if applied {
				blocked := *account
				blocked.Schedulable = false
				s.notifyAccountSchedulingBlocked(&blocked, time.Time{}, pause.Reason)
			}
		}
		return applied, err
	}
	if _, err := persist(); err != nil {
		slog.Error("account_balance_pause_persist_failed", "account_id", account.ID, "error", err)
		go func() { // Bounded retry; dispatch is fenced by generation while pending.
			for i := 0; i < 5; i++ {
				time.Sleep(time.Duration(i+1) * time.Second)
				if _, err := persist(); err == nil {
					return
				}
			}
			slog.Error("account_balance_pause_needs_attention", "account_id", account.ID)
		}()
	}
	return true
}

func (s *RateLimitService) balancePauseInFlight(a *Account) bool {
	if s == nil || a == nil {
		return false
	}
	_, ok := s.balancePausePending.Load(fmt.Sprintf("%d:%d", a.ID, AccountSchedulingGeneration(a)))
	return ok
}

// BalanceFailover runs before any passthrough/client-response policy commits
// bytes, so a definite financial error can still fall back transparently.
func (s *RateLimitService) BalanceFailover(ctx context.Context, c *gin.Context, a *Account, status int, body []byte) *UpstreamFailoverError {
	if !s.HandleAccountBalanceError(ctx, a, status, body) {
		return nil
	}
	message, _ := ClassifyAccountBalanceError(status, body)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: a.ID, AccountName: a.Name, Platform: a.Platform, UpstreamStatusCode: status, Kind: "balance_exhausted_failover", Message: message, ProxyID: opsUpstreamProxyID(a), ProxyName: opsProxyNameUnknown})
	if status == 200 {
		status = 402
	}
	return &UpstreamFailoverError{StatusCode: status, ResponseBody: body, Reason: "insufficient_account_balance", NextAccountAction: NextAccountRetry}
}
