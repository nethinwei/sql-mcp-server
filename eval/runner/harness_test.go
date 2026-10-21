package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestExecuteToolCallInvalidArgumentsStaysJSON: an argument error quoting
// the offending character still yields a valid JSON result, so the report
// marshals.
func TestExecuteToolCallInvalidArgumentsStaysJSON(t *testing.T) {
	var call toolCall
	call.Function.Name = "query_records"
	call.Function.Arguments = `{"a" "b"}`
	step, content := executeToolCall(context.Background(), nil, call)
	if !step.Denied {
		t.Fatal("invalid arguments must be denied")
	}
	var payload struct{ Error string }
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload %s is not JSON: %v", content, err)
	}
	if !strings.Contains(payload.Error, `'"'`) {
		t.Fatalf("error = %q, want the quoted character kept", payload.Error)
	}
	if _, err := json.Marshal(step); err != nil {
		t.Fatalf("step does not marshal: %v", err)
	}
}
