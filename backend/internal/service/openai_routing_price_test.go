package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountEffectiveRoutingPriceUsesAccountRateMultiplier(t *testing.T) {
	accountRate := 0.2
	account := &Account{
		RateMultiplier: &accountRate,
		Extra: map[string]any{
			"upstream_effective_rate_multiplier": 9.0,
			"upstream_group_rate_multiplier":     8.0,
		},
	}

	require.Equal(t, accountRate, openAIAccountEffectiveRoutingPrice(account))
}
