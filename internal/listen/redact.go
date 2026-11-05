package listen

import (
	"bytes"
	"encoding/json"
	"strings"
)

// sensitivePayloadKeys never reach stdout; matched by name at any depth.
var sensitivePayloadKeys = map[string]bool{
	"card_number":      true,
	"card_holder_name": true,
	"card_expiry":      true,
	"cvv":              true,
}

// redactPayload yields a placeholder, never raw bytes, for an undecodable payload.
func redactPayload(payload json.RawMessage) []byte {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return []byte("<unprintable payload>")
	}
	// Escaping off: Marshal would emit the marker as <redacted>.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactValue(decoded)); err != nil {
		return []byte("<unprintable payload>")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if sensitivePayloadKeys[strings.ToLower(k)] {
				t[k] = "<redacted>"
				continue
			}
			t[k] = redactValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = redactValue(child)
		}
		return t
	}
	return v
}
