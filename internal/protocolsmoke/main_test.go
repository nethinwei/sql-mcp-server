package main

import "testing"

// TestCheckDenial: the denial contract needs both a string code and a
// decisionId; a missing or non-string code fails.
func TestCheckDenial(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{"code":"COST_EXCEEDED","decisionId":"d1"}`: true,
		`{"decisionId":"d1"}`:                        false,
		`{"code":"","decisionId":"d1"}`:              false,
		`{"code":7,"decisionId":"d1"}`:               false,
		`{"code":"COST_EXCEEDED"}`:                   false,
		`null`:                                       false,
	} {
		if err := checkDenial([]byte(raw)); (err == nil) != ok {
			t.Errorf("checkDenial(%s) = %v, want ok=%v", raw, err, ok)
		}
	}
}
