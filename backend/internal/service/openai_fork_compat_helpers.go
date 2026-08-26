package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIMissingUsageLogInterval = time.Minute //nolint:unused

type openAIMissingUsageLogSampler struct { //nolint:unused
	total      atomic.Uint64
	suppressed atomic.Uint64
	lastLog    atomic.Int64
}

func (s *openAIMissingUsageLogSampler) sample(now time.Time) (bool, uint64, uint64) { //nolint:unused
	total := s.total.Add(1)
	nowNanos := now.UnixNano()
	for {
		last := s.lastLog.Load()
		if last != 0 && nowNanos-last < int64(openAIMissingUsageLogInterval) {
			s.suppressed.Add(1)
			return false, total, 0
		}
		if s.lastLog.CompareAndSwap(last, nowNanos) {
			return true, total, s.suppressed.Swap(0)
		}
	}
}

type responsesStreamOutputItems struct{ items map[int]json.RawMessage }

func newResponsesStreamOutputItems() *responsesStreamOutputItems {
	return &responsesStreamOutputItems{items: make(map[int]json.RawMessage)}
}
func (r *responsesStreamOutputItems) Observe(data []byte) {
	if r == nil || !gjson.ValidBytes(data) || gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(data, "item")
	if item.Exists() && item.IsObject() {
		r.items[int(gjson.GetBytes(data, "output_index").Int())] = json.RawMessage(append([]byte(nil), item.Raw...))
	}
}
func (r *responsesStreamOutputItems) HasItems() bool { return r != nil && len(r.items) > 0 }
func (r *responsesStreamOutputItems) Count() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}
func (r *responsesStreamOutputItems) BuildOutput() ([]byte, bool) {
	if !r.HasItems() {
		return nil, false
	}
	indexes := make([]int, 0, len(r.items))
	for index := range r.items {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, r.items[index])
	}
	data, err := json.Marshal(ordered)
	return data, err == nil
}

const OpenAIHTTPContinuationUnsupportedReason = GatewayFailureReason("openai_http_continuation_unsupported")

func normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body []byte, knownStoreFalse bool) ([]byte, bool, error) {
	if !knownStoreFalse && gjson.GetBytes(body, "store").Type != gjson.False {
		return body, false, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}
	var req map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &req); err != nil {
		return body, false, err
	}
	items, ok := req["input"].([]any)
	if !ok {
		return body, false, nil
	}
	filtered := make([]any, 0, len(items))
	changed := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		typ := strings.TrimSpace(firstNonEmptyString(item["type"]))
		id := strings.TrimSpace(firstNonEmptyString(item["id"]))
		if typ == "reasoning" {
			enc, has := item["encrypted_content"].(string)
			if !has || strings.TrimSpace(enc) == "" {
				changed = true
				continue
			}
			if strings.HasPrefix(id, "rs_") {
				delete(item, "id")
				changed = true
			}
			if _, has := item["call_id"]; has {
				delete(item, "call_id")
				changed = true
			}
			if summary, has := item["summary"]; !has || summary == nil {
				item["summary"] = []any{}
				changed = true
			}
		}
		if typ == "item_reference" && strings.HasPrefix(id, "rs_") {
			changed = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !changed {
		return body, false, nil
	}
	req["input"] = filtered
	next, err := marshalOpenAIUpstreamJSON(req)
	if err != nil {
		return body, false, err
	}
	return next, true, nil
}

const openAIHTTPResponseOwnerContextKeyCompat = "openai_http_response_owner"

func SetOpenAIHTTPResponseOwner(c *gin.Context, userID, apiKeyID int64) {
	if c != nil && userID > 0 && apiKeyID > 0 {
		c.Set(openAIHTTPResponseOwnerContextKeyCompat, struct{ userID, apiKeyID int64 }{userID, apiKeyID})
	}
}

func (s *OpenAIGatewayService) ValidateOpenAIHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) (bool, error) {
	if s == nil || strings.TrimSpace(responseID) == "" || userID <= 0 || apiKeyID <= 0 {
		return false, nil
	}
	ownerUser, ownerKey, found, err := s.getOpenAIWSStateStore().GetHTTPResponseOwner(ctx, groupID, responseID)
	if err != nil || !found {
		return false, err
	}
	return ownerUser == userID || (ownerUser <= 0 && ownerKey == apiKeyID), nil
}

func (s *OpenAIGatewayService) BindOpenAIHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) error {
	if s == nil {
		return nil
	}
	return s.getOpenAIWSStateStore().BindHTTPResponseOwner(ctx, groupID, responseID, userID, apiKeyID, s.openAIWSResponseStickyTTL())
}

func ResolveOpenAIAccountUpstreamModelForRequest(account *Account, requestedModel string, requireCompact bool) string {
	return resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
}

func resolveOpenAIForwardMappedModels(account *Account, requestedModel string, requireCompact bool) (billingModel, upstreamModel string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		billingModel = requestedModel
	} else if account != nil {
		billingModel = strings.TrimSpace(account.GetMappedModel(requestedModel))
	}
	if billingModel == "" {
		billingModel = requestedModel
	}
	upstreamModel = resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = billingModel
	}
	return billingModel, upstreamModel
}

func resolveOpenAIErrorSchedulingModel(billingModel, upstreamModel string) string {
	if strings.TrimSpace(upstreamModel) != "" {
		return strings.TrimSpace(upstreamModel)
	}
	return strings.TrimSpace(billingModel)
}

func normalizeOpenAIResponsesReasoningMode(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	mode := gjson.GetBytes(body, "reasoning.mode")
	if !mode.Exists() || mode.Type != gjson.String {
		return body, false, nil
	}
	updated := body
	effort := gjson.GetBytes(body, "reasoning.effort")
	if (!effort.Exists() || effort.Type == gjson.Null || strings.TrimSpace(effort.String()) == "") && strings.EqualFold(strings.TrimSpace(mode.String()), "pro") {
		var err error
		updated, err = sjson.SetBytes(updated, "reasoning.effort", "max")
		if err != nil {
			return body, false, err
		}
	}
	updated, err := sjson.DeleteBytes(updated, "reasoning.mode")
	if err != nil {
		return body, false, err
	}
	if reasoning := gjson.GetBytes(updated, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
		updated, err = sjson.DeleteBytes(updated, "reasoning")
		if err != nil {
			return body, false, err
		}
	}
	return updated, true, nil
}
