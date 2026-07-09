package mask

import (
	"testing"
)

func TestBuiltins(t *testing.T) {
	t.Parallel()
	m := NewRuleMasker(nil)
	cases := []struct {
		rule string
		in   any
		want any
	}{
		{"email", "alice@example.com", "a***@example.com"},
		{"phone", "13800138000", "138****8000"},
		{"idcard", "110101199001011234", "110***********1234"},
		{"secret", "super-secret-token", "***"},
		{"", "passthrough", "passthrough"},
		{"unknown", "x", "x"},
		{"email", "", ""},
		{"email", 42, 42}, // non-string passes through
	}
	for _, c := range cases {
		got, err := m.Mask(c.rule, c.in)
		if err != nil {
			t.Fatalf("rule %q: unexpected err %v", c.rule, err)
		}
		if got != c.want {
			t.Errorf("rule %q: got %v, want %v", c.rule, got, c.want)
		}
	}
}

func TestCustomRuleOverridesBuiltin(t *testing.T) {
	t.Parallel()
	m := NewRuleMasker(map[string]Rule{
		"email": func(_ any) (any, error) { return "CUSTOM", nil },
	})
	got, err := m.Mask("email", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "CUSTOM" {
		t.Fatalf("got %v, want CUSTOM", got)
	}
}

func TestNilValuePasses(t *testing.T) {
	t.Parallel()
	m := NewRuleMasker(nil)
	got, err := m.Mask("email", nil)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}
