package service

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeOpenAIResponsesWebSocketCompatibilityBody keeps the fork's
// aggregate request pipeline while exposing the upstream v0.1.181 ingress
// contract used by WS v2. The detailed normalizers remain shared with the
// existing HTTP path.
func normalizeOpenAIResponsesWebSocketCompatibilityBody(body []byte, account *Account) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAI() {
		return body, false, nil
	}
	normalized := body
	changed := false
	if account.IsOpenAIOAuthLike() {
		var err error
		normalized, changed, err = normalizeOpenAIResponsesLegacyIngress(normalized)
		if err != nil {
			return body, false, err
		}
		var oauthChanged bool
		normalized, oauthChanged, err = normalizeOpenAIOAuthResponsesCompatibilityBody(normalized)
		if err != nil {
			return body, false, err
		}
		changed = changed || oauthChanged
	}
	if account.IsOpenAIOAuthLike() {
		if next, c, err := normalizeOpenAIResponsesReasoningMode(normalized); err != nil {
			return body, false, err
		} else {
			normalized, changed = next, changed || c
		}
	}
	if next, c, err := sanitizeOpenAIResponsesToolSchemasForPlatform(normalized, account.Platform); err != nil {
		return body, false, err
	} else {
		normalized, changed = next, changed || c
	}
	if account.IsOpenAIApiKey() {
		if gjson.GetBytes(normalized, "store").Type == gjson.False {
			next, c, err := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(normalized, true)
			if err != nil {
				return body, false, err
			}
			normalized, changed = next, changed || c
		}
		next, c, err := normalizeOpenAIParallelToolCallsWithoutTools(normalized)
		if err != nil {
			return body, false, err
		}
		normalized, changed = next, changed || c
	}
	if next, c, err := sanitizeOpenAIResponsesInputItemIDs(normalized); err != nil {
		return body, false, fmt.Errorf("sanitize websocket Responses input item IDs: %w", err)
	} else {
		normalized, changed = next, changed || c
	}
	if schemaBody, schemaChanged, err := normalizeOpenAIResponseFormatSchemasBody(normalized); err != nil {
		return body, false, err
	} else {
		normalized, changed = schemaBody, changed || schemaChanged
	}
	if account.IsOpenAIOAuthLike() {
		for _, field := range openAIChatGPTInternalUnsupportedFields {
			if !gjson.GetBytes(normalized, field).Exists() {
				continue
			}
			next, err := sjson.DeleteBytes(normalized, field)
			if err != nil {
				return body, false, err
			}
			normalized, changed = next, true
		}
	}
	if next, c, err := NormalizeCompactionTriggerInputOrder(normalized); err != nil {
		return body, false, err
	} else {
		normalized, changed = next, changed || c
	}
	return normalized, changed, nil
}

func normalizeOpenAIOAuthResponsesCompatibilityBody(body []byte) ([]byte, bool, error) {
	var req map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &req); err != nil {
		return body, false, err
	}
	changed := false
	if prompt, ok := req["prompt"]; ok {
		if _, has := req["input"]; !has {
			req["input"] = prompt
		}
		delete(req, "prompt")
		changed = true
	}
	if _, ok := req["commands"]; ok {
		delete(req, "commands")
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	next, err := marshalOpenAIUpstreamJSON(req)
	return next, true, err
}

func normalizeOpenAIParallelToolCallsWithoutTools(body []byte) ([]byte, bool, error) {
	if gjson.GetBytes(body, "tools.#").Int() > 0 || gjson.GetBytes(body, "input.#(type==additional_tools).tools.#").Int() > 0 {
		return body, false, nil
	}
	if !gjson.GetBytes(body, "parallel_tool_calls").Exists() {
		return body, false, nil
	}
	next, err := sjson.DeleteBytes(body, "parallel_tool_calls")
	return next, err == nil, err
}

func normalizeOpenAIResponseFormatSchemasBody(body []byte) ([]byte, bool, error) {
	if !gjson.GetBytes(body, "response_format").Exists() && !gjson.GetBytes(body, "text.format").Exists() {
		return body, false, nil
	}
	var req map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &req); err != nil {
		return body, false, err
	}
	if !normalizeOpenAIResponseFormatSchemas(req) {
		return body, false, nil
	}
	next, err := marshalOpenAIUpstreamJSON(req)
	return next, true, err
}
