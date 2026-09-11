package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAIWSModelFields extracts model fields from a client event while retaining
// duplicate/case-variant keys that a map-based decoder would silently discard.
// The first slice contains top-level model values; the second contains
// session.model values for session.update frames.
func OpenAIWSModelFields(payload []byte) (topLevel, session []string, err error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	var tok json.Token
	if tok, err = dec.Token(); err != nil {
		return nil, nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("websocket payload must be an object")
	}
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, nil, keyErr
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("invalid websocket object key")
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "model":
			var value string
			if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
				topLevel = append(topLevel, strings.TrimSpace(value))
			}
		case "session":
			values, nestedErr := openAIWSObjectStringFields(raw, "model")
			if nestedErr == nil {
				session = append(session, values...)
			}
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, nil, err
	}
	return topLevel, session, nil
}

func openAIWSObjectStringFields(payload []byte, field string) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("websocket field must be an object")
	}
	values := make([]string, 0, 1)
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, keyErr
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("invalid websocket object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSpace(key), field) {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			values = append(values, strings.TrimSpace(value))
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return values, nil
}

// OpenAIWSModelValuesConflict reports whether duplicate model keys carry
// different values (case-insensitively). Identical duplicates are tolerated.
func OpenAIWSModelValuesConflict(values []string) bool {
	if len(values) < 2 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(values[0]))
	for _, value := range values[1:] {
		if strings.ToLower(strings.TrimSpace(value)) != first {
			return true
		}
	}
	return false
}
