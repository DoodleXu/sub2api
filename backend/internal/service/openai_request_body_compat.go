package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeOpenAIResponsesReasoningContentReplay(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}
	needs := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "reasoning" && len(item.Get("content").Array()) > 0 {
			needs = true
			return false
		}
		return true
	})
	if !needs {
		return body, false, nil
	}
	var obj map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &obj); err != nil {
		return body, false, err
	}
	items, ok := obj["input"].([]any)
	if !ok {
		return body, false, nil
	}
	changed := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "reasoning" {
			continue
		}
		if content, ok := item["content"].([]any); ok && len(content) > 0 {
			delete(item, "content")
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	out, err := marshalOpenAIUpstreamJSON(obj)
	return out, true, err
}

func shouldPreserveOpenAIResponsesNoneReasoningEffort(account *Account) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAIOAuthLike() {
		return true
	}
	if !account.IsOpenAIApiKey() {
		return false
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	return baseURL == "" || isOfficialOpenAIModelsBaseURL(baseURL)
}

// filterOpenAIResponsesNoneReasoningEffortForAccount removes the catalog-only
// "none" placeholder for compatible providers while preserving official OpenAI semantics.
func filterOpenAIResponsesNoneReasoningEffortForAccount(account *Account, body []byte) ([]byte, error) {
	if len(body) == 0 || shouldPreserveOpenAIResponsesNoneReasoningEffort(account) {
		return body, nil
	}
	out := body
	for _, path := range []string{"reasoning.effort", "reasoning_effort"} {
		effort := gjson.GetBytes(out, path)
		if effort.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(effort.String()), "none") {
			continue
		}
		next, err := sjson.DeleteBytes(out, path)
		if err != nil {
			return body, fmt.Errorf("strip %s none placeholder: %w", path, err)
		}
		out = next
	}
	if reasoning := gjson.GetBytes(out, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
		next, err := sjson.DeleteBytes(out, "reasoning")
		if err != nil {
			return body, fmt.Errorf("strip empty reasoning object: %w", err)
		}
		out = next
	}
	return out, nil
}

func deleteOpenAIResponsesNoneReasoningEffortFromObject(account *Account, body map[string]any) {
	if body == nil || shouldPreserveOpenAIResponsesNoneReasoningEffort(account) {
		return
	}
	if effort, ok := body["reasoning_effort"].(string); ok && strings.EqualFold(strings.TrimSpace(effort), "none") {
		delete(body, "reasoning_effort")
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		return
	}
	if effort, ok := reasoning["effort"].(string); ok && strings.EqualFold(strings.TrimSpace(effort), "none") {
		delete(reasoning, "effort")
	}
	if len(reasoning) == 0 {
		delete(body, "reasoning")
	}
}

func explicitRequestedReasoningEffortFromBody(body []byte) string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String())
	}
	return raw
}

// CanonicalRequestedReasoningEffort extracts the client-requested effort before policy rewriting.
func CanonicalRequestedReasoningEffort(body []byte, modelCandidates ...string) *string {
	if raw := explicitRequestedReasoningEffortFromBody(body); raw != "" {
		canonical := NormalizeMaxReasoningEffort(raw)
		if canonical == "" {
			return nil
		}
		return &canonical
	}
	for _, model := range modelCandidates {
		if value := canonicalReasoningEffortFromModelSuffix(model); value != "" {
			return &value
		}
	}
	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		if value := canonicalReasoningEffortFromModelSuffix(model); value != "" {
			return &value
		}
	}
	return nil
}

func CanonicalRequestedReasoningEffortFromReqBody(reqBody map[string]any, modelCandidates ...string) *string {
	if reqBody == nil {
		return CanonicalRequestedReasoningEffort(nil, modelCandidates...)
	}
	raw := ""
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			raw = strings.TrimSpace(effort)
		}
	}
	if raw == "" {
		if effort, ok := reqBody["reasoning_effort"].(string); ok {
			raw = strings.TrimSpace(effort)
		}
	}
	if raw != "" {
		canonical := NormalizeMaxReasoningEffort(raw)
		if canonical == "" {
			return nil
		}
		return &canonical
	}
	return CanonicalRequestedReasoningEffort(nil, modelCandidates...)
}

func canonicalReasoningEffortFromModelSuffix(model string) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}
	modelID := strings.TrimSpace(model)
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	if len(parts) == 0 {
		return ""
	}
	return NormalizeMaxReasoningEffort(parts[len(parts)-1])
}
