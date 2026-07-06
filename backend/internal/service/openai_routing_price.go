package service

import "math"

const (
	openAIRoutingPriceSourceUpstreamEffective = "upstream_effective_rate_multiplier"
	openAIRoutingPriceSourceUpstreamGroup     = "upstream_group_rate_multiplier"
	openAIRoutingPriceSourceAccountRate       = "account.rate_multiplier"

	openAIRoutingPriceFallbackAccountMissing        = "account_missing"
	openAIRoutingPriceFallbackUpstreamRateMissing   = "upstream_rate_missing"
	openAIRoutingPriceFallbackUpstreamRateInvalid   = "upstream_rate_invalid"
	openAIRoutingPriceFallbackAccountRateDefaultOne = "account_rate_default_1"
)

type OpenAIRoutingPriceSource struct {
	Source         string  `json:"source"`
	RateMultiplier float64 `json:"rate_multiplier"`
	Fallback       bool    `json:"fallback,omitempty"`
	FallbackReason string  `json:"fallback_reason,omitempty"`
}

func openAIAccountEffectiveRoutingPrice(account *Account) float64 {
	return openAIAccountRoutingPriceSource(account).RateMultiplier
}

func openAIAccountRoutingPriceSource(account *Account) OpenAIRoutingPriceSource {
	if account == nil {
		return OpenAIRoutingPriceSource{
			Source:         openAIRoutingPriceSourceAccountRate,
			RateMultiplier: 1,
			Fallback:       true,
			FallbackReason: openAIRoutingPriceFallbackAccountMissing,
		}
	}
	if price, ok := account.GetExtraFloat64("upstream_effective_rate_multiplier"); ok && validOpenAIRoutingPrice(price) {
		return OpenAIRoutingPriceSource{
			Source:         openAIRoutingPriceSourceUpstreamEffective,
			RateMultiplier: price,
		}
	}
	if price, ok := account.GetExtraFloat64("upstream_group_rate_multiplier"); ok && validOpenAIRoutingPrice(price) {
		return OpenAIRoutingPriceSource{
			Source:         openAIRoutingPriceSourceUpstreamGroup,
			RateMultiplier: price,
		}
	}
	price := account.BillingRateMultiplier()
	fallbackReason := openAIRoutingUpstreamRateFallbackReason(account)
	if account.RateMultiplier == nil {
		fallbackReason = openAIRoutingPriceFallbackAccountRateDefaultOne
	}
	if !validOpenAIRoutingPrice(price) {
		price = 1
		fallbackReason = openAIRoutingPriceFallbackAccountRateDefaultOne
	}
	return OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceAccountRate,
		RateMultiplier: price,
		Fallback:       true,
		FallbackReason: fallbackReason,
	}
}

func validOpenAIRoutingPrice(price float64) bool {
	if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return false
	}
	return true
}

func openAIRoutingUpstreamRateFallbackReason(account *Account) string {
	if account == nil || account.Extra == nil {
		return openAIRoutingPriceFallbackUpstreamRateMissing
	}
	for _, key := range []string{"upstream_effective_rate_multiplier", "upstream_group_rate_multiplier"} {
		if _, exists := account.Extra[key]; !exists {
			continue
		}
		if price, ok := account.GetExtraFloat64(key); !ok || !validOpenAIRoutingPrice(price) {
			return openAIRoutingPriceFallbackUpstreamRateInvalid
		}
	}
	return openAIRoutingPriceFallbackUpstreamRateMissing
}
