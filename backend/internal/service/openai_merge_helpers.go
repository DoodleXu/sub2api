package service

import (
	"encoding/json"
	"net/http"
)

func cloneOpenAIWSRawMessages(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i, m := range in {
		out[i] = append(json.RawMessage(nil), m...)
	}
	return out
}
func cloneOpenAIWSPayloadBytes(in []byte) []byte  { return append([]byte(nil), in...) }
func detectedImageContentType(data []byte) string { return http.DetectContentType(data) }
