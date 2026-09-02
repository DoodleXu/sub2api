package service

import (
	"context"
	"time"
)

// LegacyLongContextRule 平台级旧长上下文边际计费规则。
type LegacyLongContextRule struct {
	Threshold  int
	Multiplier float64
}

const (
	geminiLegacyLongContextThreshold  = 200000
	geminiLegacyLongContextMultiplier = 2.0
)

func (s *BillingService) LegacyLongContextRule(platform string) *LegacyLongContextRule {
	if platform == PlatformGemini {
		return &LegacyLongContextRule{Threshold: geminiLegacyLongContextThreshold, Multiplier: geminiLegacyLongContextMultiplier}
	}
	return nil
}

// TokenCostRequest 通用网关 token 计费请求。
type TokenCostRequest struct {
	Ctx            context.Context
	Model          string
	Group          *Group
	Tokens         UsageTokens
	RateMultiplier float64
	PricingAt      time.Time
	ServiceTier    string
	Resolver       *ModelPricingResolver
	// Resolved 为调用方预先解析的定价（Resolver.Resolve 的结果），nil 表示未解析。
	Resolved          *ResolvedPricing
	LegacyLongContext *LegacyLongContextRule
}

func legacyLongContextApplies(resolved *ResolvedPricing, group *Group, rule *LegacyLongContextRule) bool {
	if rule == nil || rule.Threshold <= 0 {
		return false
	}
	if resolved != nil && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) {
		return false
	}
	return group == nil || group.LongContextPricingEnabled
}

// CalculateTokenCostForRequest 按通用网关的路径选择计算 token 费用：
//  1. 分组/渠道显式定价，或有解析器与分组 → 统一计费
//     （区间、分组卡、目录长上下文阶梯均在其中，阶梯由目录数据驱动）；
//  2. 否则按模型目录直接计费。
//
// 模型广场的阶梯表查询与网关使用同一入口，保证展示与扣费同源。
func (s *BillingService) CalculateTokenCostForRequest(req TokenCostRequest) (*CostBreakdown, error) {
	resolved := req.Resolved
	if resolved != nil && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) {
		return s.CalculateCostUnified(s.tokenCostInput(req, resolved))
	}
	if legacyLongContextApplies(resolved, req.Group, req.LegacyLongContext) {
		return s.CalculateCostWithLongContext(req.Model, req.Tokens, req.RateMultiplier, req.LegacyLongContext.Threshold, req.LegacyLongContext.Multiplier)
	}
	if req.Resolver != nil && req.Group != nil {
		return s.CalculateCostUnified(s.tokenCostInput(req, resolved))
	}
	return s.CalculateCost(req.Model, req.Tokens, req.RateMultiplier)
}

func (s *BillingService) tokenCostInput(req TokenCostRequest, resolved *ResolvedPricing) CostInput {
	input := CostInput{
		Ctx:            req.Ctx,
		Model:          req.Model,
		Group:          req.Group,
		Tokens:         req.Tokens,
		RequestCount:   1,
		RateMultiplier: req.RateMultiplier,
		PricingAt:      req.PricingAt,
		ServiceTier:    req.ServiceTier,
		Resolver:       req.Resolver,
		Resolved:       resolved,
	}
	if req.Group != nil {
		gid := req.Group.ID
		input.GroupID = &gid
	}
	return input
}
