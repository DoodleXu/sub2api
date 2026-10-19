package service

import (
	"context"
	"time"
)

func qualityCost(a *Account) *float64 {
	if n, ok := openAIFreshUpstreamBillingRate(a, time.Now()); ok {
		return &n
	}
	return nil
}

func (s *RateLimitService) RankQualityAccounts(ctx context.Context, model string, accounts []*Account, stickyID int64) []*Account {
	if !s.QualitySchedulingEnabled(ctx) {
		return accounts
	}
	candidates := make([]AccountQualityCandidate, 0, len(accounts))
	for _, a := range accounts {
		candidates = append(candidates, AccountQualityCandidate{Account: a, Sticky: a.ID == stickyID, Cost: qualityCost(a)})
	}
	ranked := s.RankAccountQuality(ctx, model, candidates)
	out := make([]*Account, 0, len(ranked))
	for _, c := range ranked {
		if c.Summary.State != "open" && c.Account.Extra[AccountBalancePauseKey] == nil && !s.balancePauseInFlight(c.Account) {
			out = append(out, c.Account)
		}
	}
	return out
}
func (s *RateLimitService) RankQualityLoads(ctx context.Context, model string, accounts []accountWithLoad, stickyID int64) []accountWithLoad {
	if !s.QualitySchedulingEnabled(ctx) {
		return accounts
	}
	candidates := make([]AccountQualityCandidate, 0, len(accounts))
	byID := make(map[int64]accountWithLoad, len(accounts))
	for _, a := range accounts {
		load := 0.
		if a.loadInfo != nil {
			load = float64(a.loadInfo.LoadRate)
		}
		candidates = append(candidates, AccountQualityCandidate{Account: a.account, Sticky: a.account.ID == stickyID, Load: load, Cost: qualityCost(a.account)})
		byID[a.account.ID] = a
	}
	ranked := s.RankAccountQuality(ctx, model, candidates)
	out := make([]accountWithLoad, 0, len(ranked))
	for _, c := range ranked {
		if c.Summary.State != "open" && c.Account.Extra[AccountBalancePauseKey] == nil && !s.balancePauseInFlight(c.Account) {
			out = append(out, byID[c.Account.ID])
		}
	}
	return out
}
func (s *defaultOpenAIAccountScheduler) qualitySelectionOrder(ctx context.Context, req OpenAIAccountScheduleRequest, plan openAIAccountLoadPlan) []openAIAccountCandidateScore {
	candidates := make([]AccountQualityCandidate, 0, len(plan.candidates))
	byID := map[int64]openAIAccountCandidateScore{}
	for _, c := range plan.candidates {
		load := 0.
		if c.loadInfo != nil {
			load = float64(c.loadInfo.LoadRate)
		}
		candidates = append(candidates, AccountQualityCandidate{Account: c.account, Load: load, Sticky: c.account.ID == req.StickyAccountID || (req.PreviousResponseCanMove && c.account.ID == req.StickyPreviousAccountID), Cost: qualityCost(c.account)})
		byID[c.account.ID] = c
	}
	ranked := s.service.rateLimitService.RankAccountQuality(ctx, req.RequestedModel, candidates)
	out := make([]openAIAccountCandidateScore, 0, len(ranked))
	for _, c := range ranked {
		if c.Summary.State != "open" && c.Account.Extra[AccountBalancePauseKey] == nil && !s.service.rateLimitService.balancePauseInFlight(c.Account) {
			out = append(out, byID[c.Account.ID])
		}
	}
	out = append(out, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
	return out
}
