package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type OpenAIRoutingScoreBreakdown struct {
	Total     float64 `json:"total"`
	Quality   float64 `json:"quality"`
	Price     float64 `json:"price"`
	Latency   float64 `json:"latency"`
	ErrorRate float64 `json:"error_rate"`
	Priority  float64 `json:"priority"`
	Load      float64 `json:"load"`
	Queue     float64 `json:"queue"`
}

type OpenAIRoutingSummary struct {
	AccountID        int64                       `json:"account_id"`
	AccountName      string                      `json:"account_name"`
	Rank             int                         `json:"rank,omitempty"`
	Priority         int                         `json:"priority"`
	LastUsedAt       *time.Time                  `json:"last_used_at,omitempty"`
	QualityScore     int                         `json:"quality_score"`
	QualityGrade     string                      `json:"quality_grade"`
	Tier             string                      `json:"tier"`
	Score            OpenAIRoutingScoreBreakdown `json:"score"`
	StatusLabel      string                      `json:"status_label"`
	SummaryReason    string                      `json:"summary_reason"`
	SummaryReasons   []string                    `json:"summary_reasons"`
	IsSchedulableNow bool                        `json:"is_schedulable_now"`
	BlockReasons     []string                    `json:"block_reasons,omitempty"`
	SnapshotAt       time.Time                   `json:"snapshot_at"`
}

type OpenAIRoutingStrictPriorityExcludedAccount struct {
	AccountID       int64    `json:"account_id"`
	AccountName     string   `json:"account_name"`
	Priority        int      `json:"priority"`
	CurrentPriority int      `json:"current_priority"`
	Reasons         []string `json:"reasons"`
}

type OpenAIRoutingStrictPriorityExplain struct {
	Enabled                  bool                                         `json:"enabled"`
	CurrentAvailablePriority *int                                         `json:"current_available_priority,omitempty"`
	CandidateCount           int                                          `json:"candidate_count"`
	ExcludedAccounts         []OpenAIRoutingStrictPriorityExcludedAccount `json:"excluded_accounts"`
}

type OpenAIRoutingExplainParams struct {
	GroupID            *int64
	Model              string
	AccountIDs         []int64
	AccountIDsProvided bool
}

type OpenAIRoutingExplainResponse struct {
	Items             []OpenAIRoutingSummary             `json:"items"`
	Source            string                             `json:"source"`
	SchedulerStrategy string                             `json:"scheduler_strategy"`
	StrictPriority    OpenAIRoutingStrictPriorityExplain `json:"strict_priority"`
	SnapshotAt        time.Time                          `json:"snapshot_at"`
}

type OpenAIRoutingAccountExplain struct {
	Account           OpenAIRoutingSummary               `json:"account"`
	Top               []OpenAIRoutingSummary             `json:"top"`
	Notes             []string                           `json:"notes"`
	SchedulerStrategy string                             `json:"scheduler_strategy"`
	StrictPriority    OpenAIRoutingStrictPriorityExplain `json:"strict_priority"`
}

func (s *OpenAIGatewayService) ExplainOpenAIRouting(ctx context.Context, params OpenAIRoutingExplainParams) (*OpenAIRoutingExplainResponse, error) {
	now := time.Now().UTC()
	strategy := OpenAIAccountSchedulerStrategyLegacy
	if s != nil {
		strategy = NormalizeOpenAIAccountSchedulerStrategy(s.openAIAccountSchedulerStrategy(ctx))
	}
	if s == nil || s.accountRepo == nil {
		return &OpenAIRoutingExplainResponse{
			Items:             []OpenAIRoutingSummary{},
			Source:            "empty",
			SchedulerStrategy: strategy,
			StrictPriority:    openAIRoutingEmptyStrictPriorityExplain(strategy == OpenAIAccountSchedulerStrategyStrictPriority),
			SnapshotAt:        now,
		}, nil
	}

	accounts, err := s.listOpenAIRoutingExplainAccounts(ctx, params)
	if err != nil {
		return nil, err
	}
	loadMap := s.openAIRoutingLoadMap(ctx, accounts)
	summaries := make([]OpenAIRoutingSummary, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAI() {
			continue
		}
		summaries = append(summaries, s.explainOpenAIRoutingAccount(ctx, account, loadMap[account.ID], params, now, strategy))
	}
	strictPriority := applyOpenAIRoutingStrictPriorityExplain(summaries, strategy == OpenAIAccountSchedulerStrategyStrictPriority)
	s.rankOpenAIRoutingSummaries(strategy, summaries)
	return &OpenAIRoutingExplainResponse{
		Items:             summaries,
		Source:            openAIRoutingExplainSource(strategy),
		SchedulerStrategy: strategy,
		StrictPriority:    strictPriority,
		SnapshotAt:        now,
	}, nil
}

func (s *OpenAIGatewayService) listOpenAIRoutingExplainAccounts(ctx context.Context, params OpenAIRoutingExplainParams) ([]Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, nil
	}
	if params.AccountIDsProvided {
		if len(params.AccountIDs) == 0 {
			return []Account{}, nil
		}
		accounts, err := s.accountRepo.GetByIDs(ctx, params.AccountIDs)
		if err != nil {
			return nil, err
		}
		result := make([]Account, 0, len(accounts))
		for _, account := range accounts {
			if account == nil || !account.IsOpenAI() {
				continue
			}
			if params.GroupID != nil && !openAIStickyAccountMatchesGroup(account, params.GroupID) {
				continue
			}
			result = append(result, *account)
		}
		return result, nil
	}
	if params.GroupID != nil {
		return s.accountRepo.ListByGroup(ctx, *params.GroupID)
	}
	accounts, _, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10000}, PlatformOpenAI, "", "", "", 0, "")
	return accounts, err
}

func (s *OpenAIGatewayService) ExplainOpenAIRoutingForAccount(ctx context.Context, accountID int64, params OpenAIRoutingExplainParams) (*OpenAIRoutingAccountExplain, error) {
	ranking, err := s.ExplainOpenAIRouting(ctx, params)
	if err != nil {
		return nil, err
	}
	var selected *OpenAIRoutingSummary
	for i := range ranking.Items {
		if ranking.Items[i].AccountID == accountID {
			selected = &ranking.Items[i]
			break
		}
	}
	if selected == nil {
		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, infraerrors.NotFound("OPENAI_ROUTING_ACCOUNT_NOT_FOUND", "OpenAI account not found")
		}
		loadMap := s.openAIRoutingLoadMap(ctx, []Account{*account})
		fallback := s.explainOpenAIRoutingAccount(ctx, account, loadMap[account.ID], params, ranking.SnapshotAt, ranking.SchedulerStrategy)
		selected = &fallback
	}
	top := openAIRoutingTopCandidates(ranking.SchedulerStrategy, ranking.Items)
	notes := openAIRoutingNotes(ranking.SchedulerStrategy)
	return &OpenAIRoutingAccountExplain{
		Account:           *selected,
		Top:               top,
		Notes:             notes,
		SchedulerStrategy: ranking.SchedulerStrategy,
		StrictPriority:    ranking.StrictPriority,
	}, nil
}

func (s *OpenAIGatewayService) explainOpenAIRoutingAccount(ctx context.Context, account *Account, loadInfo *AccountLoadInfo, params OpenAIRoutingExplainParams, now time.Time, strategy string) OpenAIRoutingSummary {
	reasons := s.openAIRoutingBlockReasons(ctx, account, params, now, strategy)
	errorRate, ttft, hasTTFT := 0.0, 0.0, false
	if s != nil && s.openaiAccountStats != nil && account != nil {
		errorRate, ttft, hasTTFT = s.openaiAccountStats.snapshot(account.ID)
	}
	score := openAIRoutingScore(account, loadInfo, errorRate, ttft, hasTTFT)
	qualityScore := int(math.Round(score.Quality * 100))
	summaryReasons := openAIRoutingSummaryReasons(score, reasons)
	status := "candidate"
	if len(reasons) > 0 {
		status = "skipped"
	}
	return OpenAIRoutingSummary{
		AccountID:        account.ID,
		AccountName:      account.Name,
		Priority:         account.Priority,
		LastUsedAt:       account.LastUsedAt,
		QualityScore:     qualityScore,
		QualityGrade:     openAIRoutingQualityGrade(qualityScore),
		Tier:             openAIRoutingTier(qualityScore, len(reasons) == 0),
		Score:            score,
		StatusLabel:      status,
		SummaryReason:    firstString(summaryReasons, "balanced"),
		SummaryReasons:   summaryReasons,
		IsSchedulableNow: len(reasons) == 0,
		BlockReasons:     reasons,
		SnapshotAt:       now,
	}
}

func openAIRoutingEmptyStrictPriorityExplain(enabled bool) OpenAIRoutingStrictPriorityExplain {
	return OpenAIRoutingStrictPriorityExplain{
		Enabled:          enabled,
		ExcludedAccounts: []OpenAIRoutingStrictPriorityExcludedAccount{},
	}
}

func applyOpenAIRoutingStrictPriorityExplain(items []OpenAIRoutingSummary, enabled bool) OpenAIRoutingStrictPriorityExplain {
	diagnostic := openAIRoutingEmptyStrictPriorityExplain(enabled)
	if !enabled {
		return diagnostic
	}

	currentPrioritySet := false
	currentPriority := 0
	for i := range items {
		if !items[i].IsSchedulableNow {
			continue
		}
		diagnostic.CandidateCount++
		if !currentPrioritySet || items[i].Priority < currentPriority {
			currentPriority = items[i].Priority
			currentPrioritySet = true
		}
	}
	if !currentPrioritySet {
		return diagnostic
	}
	diagnostic.CurrentAvailablePriority = &currentPriority

	for i := range items {
		if !items[i].IsSchedulableNow || items[i].Priority <= currentPriority {
			continue
		}
		items[i].IsSchedulableNow = false
		items[i].StatusLabel = "skipped"
		items[i].Tier = "skipped"
		items[i].SummaryReason = "strict_priority_lower_tier"
		items[i].SummaryReasons = append([]string{"strict_priority_lower_tier"}, items[i].SummaryReasons...)
		items[i].BlockReasons = append(items[i].BlockReasons, "strict_priority_lower_tier")
		diagnostic.ExcludedAccounts = append(diagnostic.ExcludedAccounts, OpenAIRoutingStrictPriorityExcludedAccount{
			AccountID:       items[i].AccountID,
			AccountName:     items[i].AccountName,
			Priority:        items[i].Priority,
			CurrentPriority: currentPriority,
			Reasons:         []string{"strict_priority_lower_tier"},
		})
	}
	return diagnostic
}

func (s *OpenAIGatewayService) openAIRoutingBlockReasons(ctx context.Context, account *Account, params OpenAIRoutingExplainParams, now time.Time, strategy string) []string {
	if account == nil {
		return []string{"account_missing"}
	}
	reasons := make([]string, 0, 4)
	if account.IsArchived() {
		reasons = append(reasons, "archived")
	}
	if account.Status != StatusActive {
		reasons = append(reasons, "status_"+account.Status)
	}
	if !account.Schedulable {
		reasons = append(reasons, "manual_unschedulable")
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		reasons = append(reasons, "expired")
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		reasons = append(reasons, "rate_limited")
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		reasons = append(reasons, "overloaded")
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		reasons = append(reasons, "temp_unschedulable")
	}
	if strategy == OpenAIAccountSchedulerStrategyExperimental && s != nil && s.isOpenAIExperimentalCircuitOpen(account.ID, now) {
		reasons = append(reasons, "experimental_circuit_open")
	}
	if strategy != OpenAIAccountSchedulerStrategyStrictPriority && s != nil && s.isOpenAIAccountRuntimeBlocked(account) {
		reasons = append(reasons, "runtime_blocked")
	}
	if params.GroupID != nil && !openAIStickyAccountMatchesGroup(account, params.GroupID) {
		reasons = append(reasons, "group_mismatch")
	}
	if params.Model != "" && shouldClearStickySession(account, params.Model) {
		reasons = append(reasons, "model_unsupported")
	}
	return reasons
}

func (s *OpenAIGatewayService) isOpenAIExperimentalCircuitOpen(accountID int64, now time.Time) bool {
	if s == nil || s.openaiAccountStats == nil || accountID <= 0 {
		return false
	}
	state, _, _ := s.openaiAccountStats.experimentalCircuitSnapshot(accountID, now)
	return state == openAIExperimentalCircuitStateOpen
}

func (s *OpenAIGatewayService) openAIRoutingLoadMap(ctx context.Context, accounts []Account) map[int64]*AccountLoadInfo {
	loadMap := map[int64]*AccountLoadInfo{}
	if s == nil || s.concurrencyService == nil || len(accounts) == 0 {
		return loadMap
	}
	req := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		req = append(req, AccountWithConcurrency{ID: accounts[i].ID, MaxConcurrency: accounts[i].EffectiveLoadFactor()})
	}
	if batch, err := s.concurrencyService.GetAccountsLoadBatch(ctx, req); err == nil && batch != nil {
		return batch
	}
	return loadMap
}

func (s *OpenAIGatewayService) rankOpenAIRoutingSummaries(strategy string, items []OpenAIRoutingSummary) {
	if strategy == OpenAIAccountSchedulerStrategyStrictPriority {
		rankOpenAIRoutingStrictPrioritySummaries(items)
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.IsSchedulableNow != b.IsSchedulableNow {
			return a.IsSchedulableNow
		}
		if a.Score.Total != b.Score.Total {
			return a.Score.Total > b.Score.Total
		}
		return a.AccountID < b.AccountID
	})
	rank := 1
	for i := range items {
		if items[i].IsSchedulableNow {
			items[i].Rank = rank
			rank++
		}
	}
}

func rankOpenAIRoutingStrictPrioritySummaries(items []OpenAIRoutingSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.IsSchedulableNow != b.IsSchedulableNow {
			return a.IsSchedulableNow
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if openAIRoutingLastUsedEarlier(a.LastUsedAt, b.LastUsedAt) {
			return true
		}
		if openAIRoutingLastUsedEarlier(b.LastUsedAt, a.LastUsedAt) {
			return false
		}
		return a.AccountID < b.AccountID
	})

	rank := 1
	for i := range items {
		if items[i].IsSchedulableNow {
			items[i].Rank = rank
			items[i].Tier = "primary"
			items[i].SummaryReason = "strict_priority_top_tier"
			items[i].SummaryReasons = []string{"strict_priority_top_tier", openAIRoutingStrictPriorityTieReason(items[i])}
			rank++
		}
	}
}

func openAIRoutingLastUsedEarlier(left, right *time.Time) bool {
	switch {
	case left == nil && right != nil:
		return true
	case left != nil && right == nil:
		return false
	case left == nil && right == nil:
		return false
	default:
		return left.Before(*right)
	}
}

func openAIRoutingStrictPriorityTieReason(item OpenAIRoutingSummary) string {
	if item.LastUsedAt == nil {
		return "strict_priority_never_used_first"
	}
	return "strict_priority_least_recently_used"
}

func openAIRoutingTopCandidates(strategy string, items []OpenAIRoutingSummary) []OpenAIRoutingSummary {
	top := make([]OpenAIRoutingSummary, 0, 5)
	for _, item := range items {
		if !item.IsSchedulableNow {
			continue
		}
		top = append(top, item)
		if len(top) >= 5 {
			break
		}
	}
	return top
}

func openAIRoutingExplainSource(strategy string) string {
	switch strategy {
	case OpenAIAccountSchedulerStrategyExperimental:
		return "experimental_scheduler_snapshot"
	case OpenAIAccountSchedulerStrategyStrictPriority:
		return "strict_priority_snapshot"
	default:
		return "scheduler_snapshot"
	}
}

func openAIRoutingNotes(strategy string) []string {
	switch strategy {
	case OpenAIAccountSchedulerStrategyExperimental:
		return []string{"experimental_scheduler", "price_uses_upstream_cost_then_account_rate_multiplier"}
	case OpenAIAccountSchedulerStrategyStrictPriority:
		return []string{"strict_priority", "strict_priority_top_tier_only", "strict_priority_same_tier_last_used"}
	default:
		return []string{"legacy_scheduler", "priority_last_used_order"}
	}
}

func openAIRoutingScore(account *Account, loadInfo *AccountLoadInfo, errorRate, ttft float64, hasTTFT bool) OpenAIRoutingScoreBreakdown {
	priceRaw := openAIAccountEffectiveRoutingPrice(account)
	price := 1 / (1 + math.Max(0, priceRaw))
	latency := 0.5
	if hasTTFT && ttft > 0 {
		latency = 1 - clamp01((ttft-300)/14700)
	}
	errScore := 1 - clamp01(errorRate)
	priority := 0.5
	if account != nil {
		priority = 1 / (1 + math.Max(0, float64(account.Priority)))
	}
	load, queue := 1.0, 1.0
	if loadInfo != nil {
		load = 1 - clamp01(float64(loadInfo.LoadRate)/100)
		queue = 1 - clamp01(float64(loadInfo.WaitingCount)/10)
	}
	quality := clamp01(errScore*0.45 + latency*0.35 + load*0.1 + queue*0.1)
	total := quality*0.35 + price*0.25 + latency*0.15 + errScore*0.15 + priority*0.10
	return OpenAIRoutingScoreBreakdown{
		Total:     roundRouting(total),
		Quality:   roundRouting(quality),
		Price:     roundRouting(price),
		Latency:   roundRouting(latency),
		ErrorRate: roundRouting(errScore),
		Priority:  roundRouting(priority),
		Load:      roundRouting(load),
		Queue:     roundRouting(queue),
	}
}

func openAIRoutingSummaryReasons(score OpenAIRoutingScoreBreakdown, blocks []string) []string {
	if len(blocks) > 0 {
		return blocks
	}
	reasons := make([]string, 0, 3)
	if score.Price >= 0.75 && score.Quality >= 0.75 {
		reasons = append(reasons, "low_price_high_quality")
	} else if score.Quality >= 0.8 {
		reasons = append(reasons, "high_quality")
	} else if score.Price >= 0.75 {
		reasons = append(reasons, "cost_advantage")
	} else {
		reasons = append(reasons, "balanced")
	}
	return reasons
}

func openAIRoutingQualityGrade(score int) string {
	switch {
	case score >= 90:
		return "S"
	case score >= 80:
		return "A"
	case score >= 65:
		return "B"
	case score >= 50:
		return "C"
	default:
		return "D"
	}
}

func openAIRoutingTier(score int, schedulable bool) string {
	if !schedulable {
		return "skipped"
	}
	if score >= 80 {
		return "primary"
	}
	if score >= 65 {
		return "backup"
	}
	return "observe"
}

func firstString(values []string, fallback string) string {
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return fallback
	}
	return values[0]
}

func roundRouting(value float64) float64 {
	return math.Round(value*1000) / 1000
}
