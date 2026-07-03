package service

import "math"

func openAIAccountEffectiveRoutingPrice(account *Account) float64 {
	if account == nil {
		return 1
	}
	if price, ok := account.GetExtraFloat64("upstream_effective_rate_multiplier"); ok && validOpenAIRoutingPrice(price) {
		return price
	}
	if price, ok := account.GetExtraFloat64("upstream_group_rate_multiplier"); ok && validOpenAIRoutingPrice(price) {
		return price
	}
	price := account.BillingRateMultiplier()
	if !validOpenAIRoutingPrice(price) {
		return 1
	}
	return price
}

func validOpenAIRoutingPrice(price float64) bool {
	if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return false
	}
	return true
}
