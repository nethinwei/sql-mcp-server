package mask

import (
	"testing"
)

func TestBuiltins(t *testing.T) {
	t.Parallel()
	m := NewRuleMasker()
	cases := []struct {
		rule string
		in   any
		want any
	}{
		{"email", "alice@example.com", "a***@example.com"},
		{"phone", "13800138000", "138****8000"},
		{"phone", int64(13800138000), "138****8000"},
		{"idcard", "110101199001011234", "110***********1234"},
		{"secret", "super-secret-token", "***"},
		{"", "passthrough", "passthrough"},
		{"unknown", "x", "x"},
		{"email", "", ""},
		{"email", nil, nil},
		// Values a rule cannot mask by format are fully redacted, never leaked.
		{"email", "not-an-email", "***"},
		{"email", 42, "***"},
		{"phone", "1234567", "***"},
		{"idcard", true, "***"},
	}
	for _, c := range cases {
		if got := m.Mask(c.rule, c.in); got != c.want {
			t.Errorf("rule %q on %v: got %v, want %v", c.rule, c.in, got, c.want)
		}
	}
}

func TestHasReportsKnownRules(t *testing.T) {
	t.Parallel()
	m := NewRuleMasker()
	if !m.Has("email") || !m.Has("secret") {
		t.Error("built-in rules should be reported present")
	}
	if m.Has("nonexistent") {
		t.Error("unknown rule should be reported absent")
	}
}
