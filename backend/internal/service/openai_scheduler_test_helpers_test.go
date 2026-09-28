package service

import (
	"context"
	"time"
)

type upstreamCostTrackingConcurrencyCache struct {
	ConcurrencyCache
	loadMap       map[int64]*AccountLoadInfo
	acquireLimits map[int64][]int
	releases      map[int64]int
	rejectAcquire bool
}

func (c *upstreamCostTrackingConcurrencyCache) AcquireAccountSlot(_ context.Context, accountID int64, maxConcurrency int, _ string) (bool, error) {
	if c.acquireLimits == nil {
		c.acquireLimits = make(map[int64][]int)
	}
	c.acquireLimits[accountID] = append(c.acquireLimits[accountID], maxConcurrency)
	return !c.rejectAcquire, nil
}
func (c *upstreamCostTrackingConcurrencyCache) ReleaseAccountSlot(_ context.Context, accountID int64, _ string) error {
	if c.releases == nil {
		c.releases = make(map[int64]int)
	}
	c.releases[accountID]++
	return nil
}
func (c *upstreamCostTrackingConcurrencyCache) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		if load := c.loadMap[account.ID]; load != nil {
			copied := *load
			out[account.ID] = &copied
		}
	}
	return out, nil
}

func upstreamCostTestAccount(id int64, status string, rate float64, receivedAt time.Time, interval time.Duration) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		UpstreamBillingProbeExtraKey: map[string]any{"status": status, "data": map[string]any{"billing_scope": "token", "resolved_rate_multiplier": rate, "effective_rate_multiplier": rate, "peak_rate_enabled": false}, "received_at": receivedAt.UTC().Format(time.RFC3339Nano), "fresh_until": receivedAt.Add(2 * interval).UTC().Format(time.RFC3339Nano), "last_attempt_at": receivedAt.UTC().Format(time.RFC3339Nano), "next_probe_at": receivedAt.Add(interval).UTC().Format(time.RFC3339Nano)},
	}}
}

func upstreamCostTestOAuthAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func (c *upstreamCostTrackingConcurrencyCache) totalAcquires() int {
	n := 0
	for _, v := range c.acquireLimits {
		n += len(v)
	}
	return n
}
func (c *upstreamCostTrackingConcurrencyCache) releaseCount(id int64) int { return c.releases[id] }
