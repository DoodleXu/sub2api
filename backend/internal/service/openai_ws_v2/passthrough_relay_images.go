package openai_ws_v2

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

// relayImageOutputTracker mirrors the gateway's image-output accounting for
// the standalone WS passthrough relay package. Partial images mark first
// output, while only final image payloads contribute to ImageCount.
type relayImageOutputTracker struct {
	seen         map[string]struct{}
	count        int
	maxDataCount int
}

func (t *relayImageOutputTracker) Count() int {
	if t == nil {
		return 0
	}
	if t.maxDataCount > t.count {
		return t.maxDataCount
	}
	return t.count
}

func (t *relayImageOutputTracker) Observe(message []byte) bool {
	if t == nil || len(message) == 0 || !gjson.ValidBytes(message) {
		return false
	}
	root := gjson.ParseBytes(message)
	hasOutput := t.addDataArray(root.Get("data"))

	switch strings.TrimSpace(root.Get("type").String()) {
	case "response.image_generation_call.partial_image":
		return strings.TrimSpace(root.Get("partial_image_b64").String()) != "" || hasOutput
	case "response.output_item.done":
		return t.addImageOutputItem(root.Get("item")) || hasOutput
	case "response.completed", "response.done":
		return t.addOutputArray(root.Get("response.output")) || hasOutput
	case "image_generation.completed":
		if item := root.Get("item"); item.Exists() {
			return t.addImageOutputItem(item) || hasOutput
		}
		if output := root.Get("output"); output.Exists() {
			if output.IsArray() {
				return t.addOutputArray(output) || hasOutput
			}
			return t.addImageOutputItem(output) || hasOutput
		}
		return t.addImageOutputItem(root) || hasOutput
	default:
		return hasOutput
	}
}

func (t *relayImageOutputTracker) addDataArray(data gjson.Result) bool {
	if t == nil || !data.IsArray() {
		return false
	}
	count := 0
	data.ForEach(func(_, item gjson.Result) bool {
		if strings.TrimSpace(item.Get("url").String()) != "" ||
			strings.TrimSpace(item.Get("b64_json").String()) != "" {
			count++
		}
		return true
	})
	if count > t.maxDataCount {
		t.maxDataCount = count
	}
	return count > 0
}

func (t *relayImageOutputTracker) addOutputArray(output gjson.Result) bool {
	if t == nil || !output.IsArray() {
		return false
	}
	found := false
	output.ForEach(func(_, item gjson.Result) bool {
		if t.addImageOutputItem(item) {
			found = true
		}
		return true
	})
	return found
}

func (t *relayImageOutputTracker) addImageOutputItem(item gjson.Result) bool {
	if t == nil || !relayImageOutputItemHasContent(item) {
		return false
	}
	result := relayImageOutputItemResult(item)
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		sum := sha256.Sum256([]byte(result))
		key = hex.EncodeToString(sum[:])
	}
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	if _, exists := t.seen[key]; !exists {
		t.seen[key] = struct{}{}
		t.count++
	}
	return true
}

func relayImageOutputItemHasContent(item gjson.Result) bool {
	if !item.Exists() || !item.IsObject() {
		return false
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "" && itemType != "image_generation_call" && itemType != "image_generation.completed" {
		return false
	}
	return relayImageOutputItemResult(item) != ""
}

func relayImageOutputItemResult(item gjson.Result) string {
	for _, field := range []string{"result", "b64_json", "url"} {
		if value := strings.TrimSpace(item.Get(field).String()); value != "" {
			return value
		}
	}
	return ""
}
