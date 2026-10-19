package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const AccountQualitySchedulingSetting = "account_quality_scheduling_enabled"
const qualityRetention = 24 * time.Hour

type AccountQualityRequest struct {
	ID         string
	Model      string
	Capability string
	Transport  string
	Effort     string
	Stream     bool
	selection  *qualitySelectionDecision
}
type qualitySelectionDecision struct {
	once    sync.Once
	explore bool
}
type qualityRequestKey struct{}
type qualityAttemptKey struct{}

func WithAccountQualityRequest(ctx context.Context, request AccountQualityRequest) context.Context {
	ctx = withAccountQualityCredentials(ctx)
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	request.selection = &qualitySelectionDecision{}
	return context.WithValue(ctx, qualityRequestKey{}, request)
}
func WithAccountQualityCapability(ctx context.Context, capability string) context.Context {
	request := qualityRequest(ctx, "")
	request.Capability = capability
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	return context.WithValue(ctx, qualityRequestKey{}, request)
}

func AccountQualityRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	q, _ := ctx.Value(qualityRequestKey{}).(AccountQualityRequest)
	return q.ID
}
func qualityRequest(ctx context.Context, model string) AccountQualityRequest {
	q, _ := ctx.Value(qualityRequestKey{}).(AccountQualityRequest)
	if effort := RequestedReasoningEffortFromContext(ctx); effort != nil && q.Effort == "" {
		q.Effort = *effort
	}
	if model != "" {
		q.Model = model
	}
	if q.Capability == "" {
		q.Capability = "generation"
	}
	if q.Transport == "" {
		q.Transport = "http"
	}
	return q
}
func AccountQualityBucket(a *Account, q AccountQualityRequest) string {
	model := a.GetMappedModel(q.Model)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%t|%s", a.Platform, model, q.Capability, q.Transport, q.Stream, q.Effort)))
	return fmt.Sprintf("account_quality:{%d}:%d:%s", a.ID, AccountSchedulingGeneration(a), hex.EncodeToString(sum[:12]))
}

// Account-wide faults share one bucket across models and transports. Successful
// attempts supply its denominator; model-scoped failures never enter it.
func accountQualityAccountBucket(a *Account) string {
	return fmt.Sprintf("account_quality:{%d}:%d:account", a.ID, AccountSchedulingGeneration(a))
}

func mergeAccountQualitySummary(bucket, account AccountQualitySummary) AccountQualitySummary {
	if account.State == "open" || account.State == "half_open" ||
		(account.State != "unknown" && qualityTier(account.State) > qualityTier(bucket.State)) {
		bucket.State = account.State
		bucket.OpenUntil = account.OpenUntil
	}
	return bucket
}

type AccountQualitySample struct {
	RecoveryEpoch int                   `json:"recovery_epoch,omitempty"`
	Dimension     AccountQualityRequest `json:"dimension"`
	ID            string                `json:"id"`
	At            int64                 `json:"at"`
	Failed        bool                  `json:"failed"`
	Transient     bool                  `json:"transient"`
	LatencyMS     int64                 `json:"latency_ms,omitempty"`
	DurationMS    int64                 `json:"duration_ms,omitempty"`
}
type AccountQualityState struct {
	Samples           []AccountQualitySample `json:"samples"`
	OpenUntil         int64                  `json:"open_until"`
	Trips             int                    `json:"trips"`
	RecoverySuccesses int                    `json:"recovery_successes"`
	Consecutive       int                    `json:"consecutive"`
	LastFailure       int64                  `json:"last_failure"`
}
type AccountQualitySummary struct {
	State          string   `json:"state"`
	Samples        int      `json:"samples"`
	ErrorRate      *float64 `json:"error_rate,omitempty"`
	LatencySamples int      `json:"latency_samples"`
	P50MS          *int64   `json:"p50_ms,omitempty"`
	P95MS          *int64   `json:"p95_ms,omitempty"`
	UpdatedAt      int64    `json:"updated_at"`
	OpenUntil      int64    `json:"open_until,omitempty"`
	Window         string   `json:"window"`
}
type AccountQualityStore interface {
	ReadAccountQuality(context.Context, []string) (map[string]AccountQualityState, error)
	RecordAccountQuality(context.Context, string, AccountQualitySample) (AccountQualityState, error)
	AcquireAccountQualityPermit(context.Context, string, string, bool) (bool, error)
	ReleaseAccountQualityPermit(context.Context, string, string) error
}

func SummarizeAccountQuality(state AccountQualityState, now time.Time) AccountQualitySummary {
	recent := make([]AccountQualitySample, 0, len(state.Samples))
	all := make([]AccountQualitySample, 0, len(state.Samples))
	for _, v := range state.Samples {
		if v.At > now.Add(-qualityRetention).UnixMilli() {
			all = append(all, v)
			if v.At > now.Add(-15*time.Minute).UnixMilli() {
				recent = append(recent, v)
			}
		}
	}
	samples := recent
	window := "15m"
	if len(recent) < 20 {
		samples = all
		window = "24h"
	}
	out := AccountQualitySummary{State: "unknown", Samples: len(samples), OpenUntil: state.OpenUntil, Window: window}
	failures := 0
	latencies := make([]int64, 0, len(samples))
	for _, v := range samples {
		if v.At > out.UpdatedAt {
			out.UpdatedAt = v.At
		}
		if v.Failed {
			failures++
		} else if v.LatencyMS > 0 {
			latencies = append(latencies, v.LatencyMS)
		}
	}
	if len(samples) >= 20 {
		rate := float64(failures) / float64(len(samples))
		out.ErrorRate = &rate
		out.State = "healthy"
		if rate > .05 {
			out.State = "degraded"
		}
		if rate > .20 {
			out.State = "poor"
		}
	}
	out.LatencySamples = len(latencies)
	if len(latencies) >= 10 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[(len(latencies)-1)/2]
		p95 := latencies[int(math.Ceil(float64(len(latencies))*.95))-1]
		out.P50MS = &p50
		out.P95MS = &p95
	}
	if state.OpenUntil > now.UnixMilli() {
		out.State = "open"
	} else if state.Trips > 0 {
		out.State = "half_open"
	}
	return out
}

// AdvanceAccountQualityState is shared by Redis CAS and the outage fallback.
func AdvanceAccountQualityState(state AccountQualityState, v AccountQualitySample) AccountQualityState {
	for _, old := range state.Samples {
		if old.ID == v.ID {
			return state
		}
	}
	samples := make([]AccountQualitySample, 0, min(200, len(state.Samples)+1))
	for _, old := range state.Samples {
		if old.At > v.At-qualityRetention.Milliseconds() {
			samples = append(samples, old)
		}
	}
	samples = append(samples, v)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At < samples[j].At })
	if len(samples) > 200 {
		samples = samples[len(samples)-200:]
	}
	state.Samples = samples
	if !v.Failed {
		state.Consecutive = 0
		if state.Trips > 0 && v.At >= state.OpenUntil && v.RecoveryEpoch == state.Trips {
			state.RecoverySuccesses++
			if state.RecoverySuccesses >= 2 {
				state.Trips = 0
				state.OpenUntil = 0
				state.RecoverySuccesses = 0
			}
		}
		return state
	}
	state.RecoverySuccesses = 0
	if !v.Transient {
		state.Consecutive = 0
		return state
	}
	state.Consecutive = 0
	for i := len(samples) - 1; i >= 0; i-- {
		old := samples[i]
		if old.At < v.At-time.Minute.Milliseconds() || !old.Failed || !old.Transient {
			break
		}
		state.Consecutive++
	}
	state.LastFailure = v.At

	n, failed := 0, 0
	for _, sample := range samples {
		if sample.At > v.At-(15*time.Minute).Milliseconds() {
			n++
			if sample.Failed && sample.Transient {
				failed++
			}
		}
	}
	if state.Consecutive >= 3 || (n >= 20 && failed*2 >= n) || (state.Trips > 0 && v.At >= state.OpenUntil) {
		cooldown := time.Minute * time.Duration(1<<min(state.Trips, 3))
		if cooldown > 5*time.Minute {
			cooldown = 5 * time.Minute
		}
		if v.At >= state.OpenUntil {
			state.Trips++
			state.OpenUntil = v.At + cooldown.Milliseconds()
		}
		state.RecoverySuccesses = 0
	}
	return state
}

type cachedQualityState struct {
	state AccountQualityState
	at    time.Time
}
type accountQualityService struct {
	store      AccountQualityStore
	mu         sync.Mutex
	states     map[string]cachedQualityState
	leases     map[string]time.Time
	selections atomic.Uint64
	settingsMu sync.Mutex
	enabled    bool
	settingsAt time.Time
}

func newAccountQualityService(cache TempUnschedCache) *accountQualityService {
	store, _ := cache.(AccountQualityStore)
	return &accountQualityService{store: store, states: make(map[string]cachedQualityState), leases: make(map[string]time.Time), enabled: true}
}
func (s *RateLimitService) QualitySchedulingEnabled(ctx context.Context) bool {
	if s == nil || s.quality == nil {
		return false
	}
	q := s.quality
	q.settingsMu.Lock()
	defer q.settingsMu.Unlock()
	if time.Since(q.settingsAt) > 5*time.Second {
		if s.settingService != nil && s.settingService.settingRepo != nil {
			readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			raw, err := s.settingService.settingRepo.GetValue(readCtx, AccountQualitySchedulingSetting)
			cancel()
			if err == nil {
				q.enabled = strings.TrimSpace(raw) != "false"
			} else if errors.Is(err, ErrSettingNotFound) {
				q.enabled = true
			}
		}
		q.settingsAt = time.Now()
	}
	return q.enabled
}
func (q *accountQualityService) snapshots(ctx context.Context, keys []string) map[string]AccountQualityState {
	now := time.Now()
	out := make(map[string]AccountQualityState, len(keys))
	missing := make([]string, 0)
	q.mu.Lock()
	for _, key := range keys {
		v := q.states[key]
		out[key] = v.state
		if now.Sub(v.at) > 2*time.Second {
			missing = append(missing, key)
		}
	}
	q.mu.Unlock()
	if len(missing) > 0 && q.store != nil {
		readCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		values, err := q.store.ReadAccountQuality(readCtx, missing)
		cancel()
		if err == nil {
			q.mu.Lock()
			for _, key := range missing {
				state := values[key]
				q.states[key] = cachedQualityState{state, now}
				out[key] = state
			}
			q.mu.Unlock()
		}
	}
	return out
}
func (q *accountQualityService) observe(ctx context.Context, key string, sample AccountQualitySample) {
	var state AccountQualityState
	var err error
	if q.store != nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
		state, err = q.store.RecordAccountQuality(writeCtx, key, sample)
		cancel()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.store == nil || err != nil {
		state = AdvanceAccountQualityState(q.states[key].state, sample)
	}
	q.states[key] = cachedQualityState{state, time.Now()}
	if len(q.states) > 4096 {
		for k, v := range q.states {
			if time.Since(v.at) > qualityRetention {
				delete(q.states, k)
			}
		}
	}
	if err != nil {
		slog.Warn("account_quality_record_failed", "error", err)
	}
}

type AccountQualityCandidate struct {
	Account *Account
	Load    float64
	Sticky  bool
	Cost    *float64
	Summary AccountQualitySummary
}

func qualityTier(state string) int {
	switch state {
	case "healthy":
		return 0
	case "unknown", "half_open":
		return 1
	case "degraded":
		return 2
	case "poor":
		return 3
	default:
		return 4
	}
}

func (s *RateLimitService) RankAccountQuality(ctx context.Context, model string, candidates []AccountQualityCandidate) []AccountQualityCandidate {
	if len(candidates) == 0 || !s.QualitySchedulingEnabled(ctx) {
		return candidates
	}
	keys := make([]string, len(candidates)*2)
	req := qualityRequest(ctx, model)
	for i, c := range candidates {
		dimension := req
		if dimension.Transport == "websocket" && (c.Account.Platform == PlatformGrok || (s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled && c.Account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault) == OpenAIWSIngressModeHTTPBridge)) {
			dimension.Transport = "websocket_http_bridge"
		}
		keys[i] = AccountQualityBucket(c.Account, dimension)
		keys[len(candidates)+i] = accountQualityAccountBucket(c.Account)
	}
	states := s.quality.snapshots(ctx, keys)
	now := time.Now()
	fastest := map[int]int64{}
	latencyMissing := map[int]bool{}
	for i := range candidates {
		c := &candidates[i]
		c.Summary = mergeAccountQualitySummary(SummarizeAccountQuality(states[keys[i]], now), SummarizeAccountQuality(states[keys[len(candidates)+i]], now))
		tier := qualityTier(c.Summary.State)
		if c.Summary.P50MS == nil {
			latencyMissing[tier] = true
		}
		if c.Summary.P50MS != nil && (fastest[tier] == 0 || *c.Summary.P50MS < fastest[tier]) {
			fastest[tier] = *c.Summary.P50MS
		}
	}
	// Total order: latency bands are relative to a fixed fastest sample, avoiding
	// non-transitive pairwise 20% comparisons in sort.Slice.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		ta, tb := qualityTier(a.Summary.State), qualityTier(b.Summary.State)
		if ta != tb {
			return ta < tb
		}
		band := func(c AccountQualityCandidate) int {
			if latencyMissing[ta] {
				return 0
			}
			if c.Summary.P50MS == nil {
				return 1
			}
			if float64(*c.Summary.P50MS) <= float64(fastest[ta])*1.2 {
				return 0
			}
			return 2
		}
		ba, bb := band(a), band(b)
		if ba != bb {
			return ba < bb
		}
		if ba == 2 {
			if *a.Summary.P50MS != *b.Summary.P50MS {
				return *a.Summary.P50MS < *b.Summary.P50MS
			}
			if *a.Summary.P95MS != *b.Summary.P95MS {
				return *a.Summary.P95MS < *b.Summary.P95MS
			}
		}
		if a.Load != b.Load {
			return a.Load < b.Load
		}
		if (a.Cost == nil) != (b.Cost == nil) {
			return a.Cost != nil
		}
		if a.Cost != nil && b.Cost != nil && *a.Cost != *b.Cost {
			return *a.Cost < *b.Cost
		}
		if a.Sticky != b.Sticky {
			return a.Sticky
		}
		if a.Account.Priority != b.Account.Priority {
			return a.Account.Priority < b.Account.Priority
		}
		if a.Account.LastUsedAt == nil {
			return b.Account.LastUsedAt != nil
		}
		if b.Account.LastUsedAt == nil {
			return false
		}
		return a.Account.LastUsedAt.Before(*b.Account.LastUsedAt)
	})
	// One exploration slot per twenty selections; the shared dispatch lease
	// guarantees at most one unknown/recovery request per account/model bucket.
	explore := false
	if req.selection != nil {
		req.selection.once.Do(func() { req.selection.explore = s.quality.selections.Add(1)%20 == 0 })
		explore = req.selection.explore
	} else {
		explore = s.quality.selections.Add(1)%20 == 0
	}
	if explore && candidates[0].Summary.State == "healthy" {
		for i := 1; i < len(candidates); i++ {
			if candidates[i].Summary.State == "unknown" || candidates[i].Summary.State == "half_open" {
				c := candidates[i]
				copy(candidates[1:i+1], candidates[:i])
				candidates[0] = c
				break
			}
		}
	}
	return candidates
}
