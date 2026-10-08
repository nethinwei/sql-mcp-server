package configyaml

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// ruleCase is one entry of core/config/testdata/rules.json, shared with the
// console's validator tests (web/admin/src/lib/rules.test.ts).
type ruleCase struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
	// Error is a substring of the server's error; empty means valid.
	Error string `json:"error"`
}

// The server loads every shared rule case as the case expects. JSON is YAML,
// so the configurations decode as written.
func TestSharedRuleCases(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../core/config/testdata/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []ruleCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			_, err := Decode(tc.Config)
			switch {
			case tc.Error == "" && err != nil:
				t.Fatalf("valid case rejected: %v", err)
			case tc.Error != "" && (err == nil || !strings.Contains(err.Error(), tc.Error)):
				t.Fatalf("err = %v, want %q", err, tc.Error)
			}
		})
	}
}
