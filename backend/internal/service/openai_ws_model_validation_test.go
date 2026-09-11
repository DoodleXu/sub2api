package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSModelFieldsPreservesDuplicateCaseVariantKeys(t *testing.T) {
	topLevel, session, err := OpenAIWSModelFields([]byte(`{
		"model":"gpt-5.6-sol",
		"Model":"gpt-6-astra",
		"session":{"model":"gpt-5.6-sol","MODEL":"gpt-6-astra"}
	}`))

	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, topLevel)
	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, session)
	require.True(t, OpenAIWSModelValuesConflict(topLevel))
	require.True(t, OpenAIWSModelValuesConflict(session))
}

func TestOpenAIWSModelValuesConflictAllowsIdenticalDuplicates(t *testing.T) {
	require.False(t, OpenAIWSModelValuesConflict([]string{"gpt-5.6-sol", " GPT-5.6-SOL "}))
}
