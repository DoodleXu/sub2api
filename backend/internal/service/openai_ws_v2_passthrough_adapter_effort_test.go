//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestEnsureOpenAIWSPassthroughResponseCreateEventID(t *testing.T) {
	generated, generatedID, err := ensureOpenAIWSPassthroughResponseCreateEventID(
		[]byte(`{"type":"response.create","model":"gpt-5.4"}`),
	)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(generatedID, "evt_"))
	require.Equal(t, generatedID, gjson.GetBytes(generated, "event_id").String())

	existing := []byte(`{"type":"response.create","event_id":"evt_client","model":"gpt-5.4"}`)
	preserved, preservedID, err := ensureOpenAIWSPassthroughResponseCreateEventID(existing)
	require.NoError(t, err)
	require.Equal(t, "evt_client", preservedID)
	require.Equal(t, existing, preserved)

	nonCreate := []byte(`{"type":"session.update","session":{"model":"gpt-5.4"}}`)
	unchanged, eventID, err := ensureOpenAIWSPassthroughResponseCreateEventID(nonCreate)
	require.NoError(t, err)
	require.Empty(t, eventID)
	require.Equal(t, nonCreate, unchanged)
}

func TestWSPassthroughUsageMeta_InitFromFirstFrame_MappedModelCandidate(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"sol","reasoning":{"effort":"max"}}`)

	meta := newOpenAIWSPassthroughUsageMeta("sol", body)
	meta.captureWrittenTurn(1, body, "gpt-5.6-sol")

	turn, ok := meta.takeTurn(1)
	require.True(t, ok)
	got := turn.reasoningEffort
	require.NotNil(t, got, "reasoning effort should be set")
	require.Equal(t, "max", *got, "mapped model gpt-5.6-sol should preserve max")
}

func TestWSPassthroughUsageMeta_InitFromFirstFrame_NonGPT56FallsBackToXHigh(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-5.4","reasoning":{"effort":"max"}}`)

	meta := newOpenAIWSPassthroughUsageMeta("gpt-5.4", body)
	meta.captureWrittenTurn(1, body, "gpt-5.4")

	turn, ok := meta.takeTurn(1)
	require.True(t, ok)
	got := turn.reasoningEffort
	require.NotNil(t, got)
	require.Equal(t, "xhigh", *got, "non-5.6 model should normalize max to xhigh")
}

func TestWSPassthroughUsageMeta_UpdateFromResponseCreate_MappedModelCandidate(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"sol","reasoning":{"effort":"max"}}`)

	meta := newOpenAIWSPassthroughUsageMeta("sol", body)
	meta.captureWrittenTurn(2, body, "gpt-5.6-sol")

	turn, ok := meta.takeTurn(2)
	require.True(t, ok)
	got := turn.reasoningEffort
	require.NotNil(t, got)
	require.Equal(t, "max", *got, "mapped model should preserve max on multi-turn update")
}
