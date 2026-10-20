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
		{"email", "张三@example.com", "张***@example.com"},
		{"phone", "13800138000", "138****8000"},
		{"phone", int64(13800138000), "138****8000"},
		{"idcard", "110101199001011234", "110****1234"},
		// The mask is fixed: the output does not tell the value's length.
		{"idcard", "1101011990010112341234567890", "110****7890"},
		// Short values keep at most half in the clear.
		{"phone", "12345678", "12****78"},
		// Characters, not bytes: multi-byte input stays valid UTF-8.
		{"idcard", "一二三四五六七八九十一", "一二三****八九十一"},
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
