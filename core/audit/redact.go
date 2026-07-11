package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const maxAuditInputBytes = 4096

// RedactInput removes values for configured sensitive fields and hashes
// transaction handles before tool input is queued for persistence.
func RedactInput(input json.RawMessage, sensitive map[string]bool) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	redactValue(value, sensitive)
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	if len(encoded) > maxAuditInputBytes {
		return json.RawMessage(`{"redacted":true,"reason":"input_too_large"}`)
	}
	return encoded
}

func redactValue(value any, sensitive map[string]bool) {
	switch node := value.(type) {
	case map[string]any:
		if field, ok := node["field"].(string); ok && sensitive[field] {
			if _, exists := node["value"]; exists {
				node["value"] = "***"
			}
		}
		for key, child := range node {
			switch key {
			case "transaction":
				if token, ok := child.(string); ok && token != "" {
					sum := sha256.Sum256([]byte(token))
					node[key] = "sha256:" + hex.EncodeToString(sum[:8])
				}
			case "values", "set", "cursor", "args":
				if fields, ok := child.(map[string]any); ok {
					for field := range fields {
						if sensitive[field] {
							fields[field] = "***"
						}
					}
				}
			}
			redactValue(child, sensitive)
		}
	case []any:
		for _, child := range node {
			redactValue(child, sensitive)
		}
	}
}
