package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestReasoningEffortMappingsTriState(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
		require.Nil(t, req.ReasoningEffortMappings)
	})

	t.Run("empty array means clear", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[]}`), &req))
		require.NotNil(t, req.ReasoningEffortMappings)
		require.Empty(t, *req.ReasoningEffortMappings)
	})

	t.Run("non empty array means replace", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[{"from":"max","to":"xhigh"}]}`), &req))
		require.NotNil(t, req.ReasoningEffortMappings)
		require.Len(t, *req.ReasoningEffortMappings, 1)
		require.Equal(t, "max", (*req.ReasoningEffortMappings)[0].From)
		require.Equal(t, "xhigh", (*req.ReasoningEffortMappings)[0].To)
	})
}

func TestUpdateGroupRequestModelPricingTriState(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
		require.Nil(t, req.ModelPricing)
		require.Nil(t, req.LongContextPricingEnabled)
	})

	t.Run("empty array clears pricing and false is preserved", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"long_context_pricing_enabled":false,"model_pricing":[]}`), &req))
		require.NotNil(t, req.ModelPricing)
		require.Empty(t, *req.ModelPricing)
		require.NotNil(t, req.LongContextPricingEnabled)
		require.False(t, *req.LongContextPricingEnabled)
	})
}
