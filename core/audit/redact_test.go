package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// An input with nothing to redact is kept as it is, compacted; a
// transaction handle, however its key is spelled, is still hashed.
func TestRedactInputWithNothingSensitive(t *testing.T) {
	t.Parallel()
	if got := string(RedactInput(json.RawMessage(`{ "entity": "orders",
		"filter": [ {"field":"id","op":"in","value":[1, 2]} ] }`), nil)); got !=
		`{"entity":"orders","filter":[{"field":"id","op":"in","value":[1,2]}]}` {
		t.Fatalf("redacted = %s", got)
	}
	for _, input := range []string{`{"transaction":"tok-1"}`, `{"transaction":"tok-1"}`} {
		if got := string(RedactInput(json.RawMessage(input), nil)); strings.Contains(got, "tok-1") ||
			!strings.Contains(got, "sha256:") {
			t.Fatalf("%s redacted = %s", input, got)
		}
	}
	if got := string(RedactInput(json.RawMessage(`{"entity":`), nil)); got != `{"redacted":true}` {
		t.Fatalf("invalid input = %s", got)
	}
	large := `{"values":["` + strings.Repeat("x", maxAuditInputBytes) + `"]}`
	if got := string(RedactInput(json.RawMessage(large), nil)); !strings.Contains(got, "input_too_large") {
		t.Fatalf("large input = %s", got)
	}
}

func BenchmarkRedactInput(b *testing.B) {
	values := make([]string, 256)
	for i := range values {
		values[i] = "1"
	}
	input := json.RawMessage(`{"entity":"orders","filter":[{"field":"id","op":"in","value":[` +
		strings.Join(values, ",") + `]}]}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = RedactInput(input, nil)
	}
}
