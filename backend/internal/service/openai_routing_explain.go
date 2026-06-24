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

type OpenAIRoutingExplainParams struct {
	GroupID            *int64
	Model              string
	AccountIDs         []int64
	AccountIDsProvided bool
}

type OpenAIRoutingExplainResponse struct {
	Items      []OpenAIRoutingSummary `json:"items"`
	Source     string                 `json:"source"`
	SnapshotAt time.Time              `json:"snapshot_at"`
}

type OpenAIRoutingAccountExplain struct {
	Account OpenAIRoutingSummary   `json:"account"`
	Top     []OpenAIRoutingSummary `json:"top"`
	Notes   []string               `json:"notes"`
}

func (s *OpenAIGatewayService) ExplainOpenAIRouting(ctx context.Context, params OpenAIRoutingExplainParams) (*OpenAIRoutingExplainResponse, error) {
	now := time.Now().UTC()
	if s == nil || s.accountRepo == nil {
		return &OpenAIRoutingExplainResponse{Items: []OpenAIRoutingSummary{}, Source: "empty", SnapshotAt: now}, nil
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
		summaries = append(summaries, s.explainOpenAIRoutingAccount(ctx, account, loadMap[account.ID], params, now))
	}
	s.rankOpenAIRoutingSummaries(summaries)
	return &OpenAIRoutingExplainResponse{Items: summaries, Source: "scheduler_snapshot", SnapshotAt: now}, nil
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
		fallback := s.explainOpenAIRoutingAccount(ctx, account, loadMap[account.ID], params, ranking.SnapshotAt)
		selected = &fallback
	}
	top := make([]OpenAIRoutingSummary, 0, 5)
	for _, item := range ranking.Items {
		if item.IsSchedulableNow {
			top = append(top, item)
		}
		if len(top) >= 5 {
			break
		}
	}
	notes := []string{"experimental_scheduler", "price_uses_upstream_effective_rate_multiplier_then_account_rate_multiplier"}
	return &OpenAIRoutingAccountExplain{Account: *selected, Top: top, Notes: notes}, nil
}

func (s *OpenAIGatewayService) explainOpenAIRoutingAccount(ctx context.Context, account *Account, loadInfo *AccountLoadInfo, params OpenAIRoutingExplainParams, now time.Time) OpenAIRoutingSummary {
	reasons := s.openAIRoutingBlockReasons(ctx, account, params, now)
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

func (s *OpenAIGatewayService) openAIRoutingBlockReasons(ctx context.Context, account *Account, params OpenAIRoutingExplainParams, now time.Time) []string {
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
	if s != nil && s.isOpenAIAccountRuntimeBlocked(account) {
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

func (s *OpenAIGatewayService) rankOpenAIRoutingSummaries(items []OpenAIRoutingSummary) {
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
