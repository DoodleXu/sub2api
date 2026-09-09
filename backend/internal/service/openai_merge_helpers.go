package service

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func cloneOpenAIWSRawMessages(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i, m := range in {
		out[i] = append(json.RawMessage(nil), m...)
	}
	return out
}
func cloneOpenAIWSPayloadBytes(in []byte) []byte { return append([]byte(nil), in...) }
func (s *OpenAIGatewayService) validateOutboundURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", http.ErrUseLastResponse
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && s != nil && s.cfg != nil && s.cfg.Security.URLAllowlist.AllowInsecureHTTP) {
		return "", http.ErrUseLastResponse
	}
	return u.String(), nil
}
func detectedImageContentType(data []byte) string { return http.DetectContentType(data) }
