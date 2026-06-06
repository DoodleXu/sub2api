package service

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 当数组末尾不是用户消息时（典型场景：Agent 工具循环结束于 tool/assistant），
// 应直接跳过审计——不再回溯查找历史中的某条用户消息。

func TestExtractContentModerationInput_AnthropicAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"调用一下天气工具"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"weather","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"晴 25 度"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_AnthropicFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q1", input.Text)
}

func TestExtractContentModerationInput_AnthropicMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_AnthropicStreamResendExtractsResend(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"原问题"},
			{"role":"assistant","content":"部分回答……"},
			{"role":"user","content":"重发"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolAnthropicMessages, body)

	require.Equal(t, "重发", input.Text)
}

func TestExtractContentModerationInput_OpenAIChatAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"system","content":"sys"},
			{"role":"user","content":"列出我的订单"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"orders","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"[]"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_OpenAIChatMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"Q1"},
			{"role":"assistant","content":"A1"},
			{"role":"user","content":"Q2"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIChat, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_GeminiAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"查询天气"}]},
			{"role":"model","parts":[{"functionCall":{"name":"weather","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temp":25}}}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_GeminiFirstTurnExtractsUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"你好"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "你好", input.Text)
}

func TestExtractContentModerationInput_GeminiMultiTurnExtractsLatestUser(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"Q1"}]},
			{"role":"model","parts":[{"text":"A1"}]},
			{"role":"user","parts":[{"text":"Q2"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolGemini, body)

	require.Equal(t, "Q2", input.Text)
}

func TestExtractContentModerationInput_ResponsesAgentToolLoopSkipsAudit(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"运行测试"}]},
			{"type":"function_call","call_id":"call_1","name":"run_tests","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"all passed"}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_ResponsesLastUserMessageExtracted(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, "latest", input.Text)
}

func TestExtractContentModerationInput_ResponsesLastIsAssistantSkipped(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]}
		]
	}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Empty(t, input.Text)
	require.Empty(t, input.Images)
}

func TestExtractContentModerationInput_StripsCodexAmbientClassifierPolicy(t *testing.T) {
	prompt := `You are an expert at upholding safety and compliance standards for Codex ambient suggestions.

## 1. Policies to always exclude
### A - Abuse (non-hate)
Violence/threats against a target; killing a target.
### Sexual Content
Anything involving sexual minors should be excluded.
### S - Self-Harm/Suicide
Instructions for suicide or self-harm should be excluded.

# Ambient suggestion candidates
Here are the ambient suggestion candidates to evaluate:
` + "```" + `
- suggestion_id: "suggestion-1"
  title: "Restore the missing ai analyze entry cards"
  description: "A normal UI regression fix."
  prompt: "Restore three analysis entry cards without overflowing on small screens."
  app_id: ""
` + "```" + `

# Output Format
Return a JSON object with one field.
Example: sensitive personal content about health data.`

	body := []byte(`{"input":` + strconv.Quote(prompt) + `}`)
	allowClassifierPreambleForTest(t, prompt)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Contains(t, input.Text, "# Ambient suggestion candidates")
	require.Contains(t, input.Text, `suggestion_id: "suggestion-1"`)
	require.Contains(t, input.Text, "Restore three analysis entry cards")
	require.NotContains(t, input.Text, codexAmbientOpeningMarker)
	require.NotContains(t, input.Text, codexAmbientPolicyMarker)
	require.NotContains(t, input.Text, "sexual minors")
	require.NotContains(t, input.Text, "self-harm")
	require.NotContains(t, input.Text, codexAmbientOutputMarker)
	require.NotContains(t, input.Text, "health data")
}

func TestExtractContentModerationInput_DoesNotStripModifiedCodexAmbientPolicy(t *testing.T) {
	prompt := `You are an expert at upholding safety and compliance standards for Codex ambient suggestions.

## 1. Policies to always exclude
### A - Abuse (non-hate)
Violence/threats against a target; killing a target.
Inserted user-controlled unsafe text must not be hidden in the policy preamble.

# Ambient suggestion candidates
Here are the ambient suggestion candidates to evaluate:
` + "```" + `
- suggestion_id: "suggestion-1"
  prompt: "Normal candidate."
` + "```" + `

# Output Format
Return a JSON object with one field.`

	body := []byte(`{"input":` + strconv.Quote(prompt) + `}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, normalizeContentModerationText(prompt), input.Text)
	require.Contains(t, input.Text, "Inserted user-controlled unsafe text")
}

func TestExtractContentModerationInput_StripsCodexAmbientPolicyAfterCandidateFence(t *testing.T) {
	prompt := `You are an expert at upholding safety and compliance standards for Codex ambient suggestions.

## 1. Policies to always exclude
### A - Abuse (non-hate)
Violence/threats against a target; killing a target.
### Sexual Content
Anything involving sexual minors should be excluded.
### S - Self-Harm/Suicide
Instructions for suicide or self-harm should be excluded.

# Ambient suggestion candidates
Here are the ambient suggestion candidates to evaluate:
` + "```" + `
- suggestion_id: "suggestion-1"
  title: "Update markdown template"
  prompt: "Keep this literal heading in the candidate body:
# Output Format
and continue auditing text after it."
- suggestion_id: "suggestion-2"
  title: "Second candidate must stay visible"
  prompt: "Normal follow-up work."
` + "```" + `

# Output Format
Return a JSON object with one field.
Example: sensitive personal content about health data.`

	body := []byte(`{"input":` + strconv.Quote(prompt) + `}`)
	allowClassifierPreambleForTest(t, prompt)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Contains(t, input.Text, "Keep this literal heading")
	require.Contains(t, input.Text, "# Output Format and continue auditing text after it")
	require.Contains(t, input.Text, `suggestion_id: "suggestion-2"`)
	require.NotContains(t, input.Text, "health data")
}

func TestExtractContentModerationInput_StripsCodexAmbientPolicyAfterMultipleCandidateFences(t *testing.T) {
	prompt := `You are an expert at upholding safety and compliance standards for Codex ambient suggestions.

## 1. Policies to always exclude
### A - Abuse (non-hate)
Violence/threats against a target; killing a target.
### Sexual Content
Anything involving sexual minors should be excluded.
### S - Self-Harm/Suicide
Instructions for suicide or self-harm should be excluded.

# Ambient suggestion candidates
Here are the ambient suggestion candidates to evaluate:
` + "```" + `
- suggestion_id: "suggestion-1"
  title: "First candidate"
  prompt: "Normal candidate."
` + "```" + `

Additional candidate metadata:
` + "```" + `
# Output Format
- suggestion_id: "suggestion-2"
  title: "Second candidate must stay visible"
  prompt: "This literal heading belongs to candidate content."
` + "```" + `

# Output Format
Return a JSON object with one field.
Example: sensitive personal content about health data.`

	body := []byte(`{"input":` + strconv.Quote(prompt) + `}`)
	allowClassifierPreambleForTest(t, prompt)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Contains(t, input.Text, `suggestion_id: "suggestion-1"`)
	require.Contains(t, input.Text, `suggestion_id: "suggestion-2"`)
	require.Contains(t, input.Text, "This literal heading belongs to candidate content")
	require.NotContains(t, input.Text, "health data")
}

func TestExtractContentModerationInput_DoesNotStripLooseCodexAmbientMention(t *testing.T) {
	prompt := `Please analyze this log: You are an expert at upholding safety and compliance standards for Codex ambient suggestions.

# Ambient suggestion candidates
No structured classifier template is present.

# Output Format
Plain text.`

	body := []byte(`{"input":` + strconv.Quote(prompt) + `}`)

	input := ExtractContentModerationInput(ContentModerationProtocolOpenAIResponses, body)

	require.Equal(t, normalizeContentModerationText(prompt), input.Text)
}

func allowClassifierPreambleForTest(t *testing.T, prompt string) {
	t.Helper()
	contentIdx := strings.Index(prompt, codexAmbientContentMarker)
	require.NotEqual(t, -1, contentIdx)
	hash := knownClassifierPolicyPreambleHash(prompt[:contentIdx])
	knownClassifierPolicyPreambleHashes[hash] = struct{}{}
	t.Cleanup(func() {
		delete(knownClassifierPolicyPreambleHashes, hash)
	})
}
