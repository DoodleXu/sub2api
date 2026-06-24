package service

import "math"

func openAIAccountEffectiveRoutingPrice(account *Account) float64 {
	if account == nil {
		return 1
	}
	if v, ok := account.GetExtraFloat64("upstream_effective_rate_multiplier"); ok && v >= 0 {
		return v
	}
	if v, ok := account.GetExtraFloat64("upstream_group_rate_multiplier"); ok && v >= 0 {
		return v
	}
	price := account.BillingRateMultiplier()
	if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return 1
	}
	return price
}
