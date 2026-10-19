package service

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sync"
)

type accountQualityCredentialsKey struct{}

type accountQualityCredentialRefresh struct {
	before *Account
	after  *Account
}

// Refresh authorizations belong to one downstream request. They also cover
// token acquisition before the first WS turn, without mutating cached accounts.
type accountQualityCredentials struct {
	mu        sync.RWMutex
	refreshed map[int64]accountQualityCredentialRefresh
}

func withAccountQualityCredentials(ctx context.Context) context.Context {
	if _, ok := ctx.Value(accountQualityCredentialsKey{}).(*accountQualityCredentials); ok {
		return ctx
	}
	return context.WithValue(ctx, accountQualityCredentialsKey{}, &accountQualityCredentials{refreshed: make(map[int64]accountQualityCredentialRefresh)})
}

func accountQualityCredentialSnapshot(ctx context.Context, account *Account) *Account {
	state, _ := ctx.Value(accountQualityCredentialsKey{}).(*accountQualityCredentials)
	if state == nil || account == nil {
		return account
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	refresh, ok := state.refreshed[account.ID]
	if ok && (sameAccountQualityCredentials(account, refresh.before) || sameAccountQualityCredentials(account, refresh.after)) {
		return refresh.after
	}
	return account
}

func sameAccountQualityCredentials(a, b *Account) bool {
	return a != nil && b != nil && a.ID == b.ID && a.Platform == b.Platform && a.Type == b.Type &&
		AccountSchedulingGeneration(a) == AccountSchedulingGeneration(b) && equalAccountQualityCredentials(a.Credentials, b.Credentials) &&
		reflect.DeepEqual(a.ProxyID, b.ProxyID)
}

func equalAccountQualityCredentials(a, b map[string]any) bool {
	// JSON persistence turns int64 timestamps into float64 on reload. Compare
	// their wire representation, including every credential and identity field.
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func snapshotAccountQualityCredentials(account *Account) *Account {
	copy := snapshotOAuthRefreshAccount(account)
	copy.Extra = shallowCopyMap(account.Extra)
	return copy
}

// Only a successful refresh whose durable row still contains exactly the
// issued credentials can authorize its new generation. Extra generation steps
// (manual resume, another credential replacement) remain fenced.
func authorizeAccountQualityCredentialRefresh(ctx context.Context, before, after *Account, issued map[string]any) error {
	state, _ := ctx.Value(accountQualityCredentialsKey{}).(*accountQualityCredentials)
	if state == nil {
		return nil
	}
	valid := before != nil && after != nil && before.ID == after.ID && before.Platform == after.Platform && before.Type == after.Type &&
		reflect.DeepEqual(before.ProxyID, after.ProxyID) && equalAccountQualityCredentials(issued, after.Credentials)
	if valid {
		delta := AccountSchedulingGeneration(after) - AccountSchedulingGeneration(before)
		valid = delta >= 0 && delta <= 1
	}
	if !valid {
		return &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeAccount, Reason: "scheduling_state_changed"}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	origin := before
	if prior, ok := state.refreshed[before.ID]; ok {
		if !sameAccountQualityCredentials(prior.after, before) {
			return &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeAccount, Reason: "scheduling_state_changed"}
		}
		origin = prior.before
	}
	state.refreshed[before.ID] = accountQualityCredentialRefresh{
		before: snapshotAccountQualityCredentials(origin), after: snapshotAccountQualityCredentials(after),
	}
	return nil
}
