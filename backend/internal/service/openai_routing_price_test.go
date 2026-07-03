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

	account.Extra = nil
	require.Equal(t, accountRate, openAIAccountEffectiveRoutingPrice(account))
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
}
