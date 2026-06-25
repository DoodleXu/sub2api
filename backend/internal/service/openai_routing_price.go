package service

import "math"

func openAIAccountEffectiveRoutingPrice(account *Account) float64 {
	if account == nil {
		return 1
	}
	price := account.BillingRateMultiplier()
	if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return 1
	}
	return price
}
