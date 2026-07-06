package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountEffectiveRoutingPriceUsesUpstreamEffectiveRateMultiplierFirst(t *testing.T) {
	accountRate := 0.2
	account := &Account{
		RateMultiplier: &accountRate,
		Extra: map[string]any{
			"upstream_effective_rate_multiplier": 0.08,
			"upstream_group_rate_multiplier":     0.12,
		},
	}

	require.Equal(t, 0.08, openAIAccountEffectiveRoutingPrice(account))
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceUpstreamEffective,
		RateMultiplier: 0.08,
	}, openAIAccountRoutingPriceSource(account))
}

func TestOpenAIAccountEffectiveRoutingPriceFallsBackToGroupThenAccountRate(t *testing.T) {
	accountRate := 0.2
	account := &Account{
		RateMultiplier: &accountRate,
		Extra: map[string]any{
			"upstream_effective_rate_multiplier": -1.0,
			"upstream_group_rate_multiplier":     "0.12",
		},
	}

	require.Equal(t, 0.12, openAIAccountEffectiveRoutingPrice(account))
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceUpstreamGroup,
		RateMultiplier: 0.12,
	}, openAIAccountRoutingPriceSource(account))

	account.Extra = nil
	require.Equal(t, accountRate, openAIAccountEffectiveRoutingPrice(account))
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceAccountRate,
		RateMultiplier: accountRate,
		Fallback:       true,
		FallbackReason: openAIRoutingPriceFallbackUpstreamRateMissing,
	}, openAIAccountRoutingPriceSource(account))
}

func TestOpenAIAccountEffectiveRoutingPriceAllowsZeroUpstreamCost(t *testing.T) {
	accountRate := 0.2
	account := &Account{
		RateMultiplier: &accountRate,
		Extra: map[string]any{
			"upstream_effective_rate_multiplier": 0.0,
		},
	}

	require.Equal(t, 0.0, openAIAccountEffectiveRoutingPrice(account))
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceUpstreamEffective,
		RateMultiplier: 0.0,
	}, openAIAccountRoutingPriceSource(account))
}

func TestOpenAIAccountEffectiveRoutingPriceDefaultAccountRateScoresAsHalf(t *testing.T) {
	account := &Account{}

	require.Equal(t, 1.0, openAIAccountEffectiveRoutingPrice(account))
	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceAccountRate,
		RateMultiplier: 1.0,
		Fallback:       true,
		FallbackReason: openAIRoutingPriceFallbackAccountRateDefaultOne,
	}, openAIAccountRoutingPriceSource(account))
	require.Equal(t, 0.5, openAIRoutingScore(account, nil, 0, 0, false).Price)
}

func TestOpenAIAccountEffectiveRoutingPriceMarksInvalidUpstreamFallback(t *testing.T) {
	accountRate := 0.2
	account := &Account{
		RateMultiplier: &accountRate,
		Extra: map[string]any{
			"upstream_effective_rate_multiplier": "not-a-number",
			"upstream_group_rate_multiplier":     -1,
		},
	}

	require.Equal(t, OpenAIRoutingPriceSource{
		Source:         openAIRoutingPriceSourceAccountRate,
		RateMultiplier: accountRate,
		Fallback:       true,
		FallbackReason: openAIRoutingPriceFallbackUpstreamRateInvalid,
	}, openAIAccountRoutingPriceSource(account))
}
