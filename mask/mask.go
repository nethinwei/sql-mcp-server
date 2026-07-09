package mask

import (
	"fmt"
	"strings"
)

// Masker applies a named masking rule to a value. Implementations must not
// panic on unknown rules or nil values.
type Masker interface {
	Mask(rule string, value any) (any, error)
}

// NoopMasker passes values through unchanged; used when masking is disabled.
type NoopMasker struct{}

// Mask implements Masker by returning the value unchanged.
func (NoopMasker) Mask(_ string, value any) (any, error) { return value, nil }

// Rule masks a single value. It returns the masked value; errors are reserved
// for rule-internal failures (most rules never error).
type Rule func(value any) (any, error)

// RuleMasker maps rule names to Rules. The zero value masks nothing.
type RuleMasker struct {
	rules map[string]Rule
}

// NewRuleMasker returns a Masker with the built-in rules (email, phone, idcard,
// secret) plus any custom rules. Custom rules override built-ins on name clash.
func NewRuleMasker(custom map[string]Rule) *RuleMasker {
	m := &RuleMasker{rules: builtins()}
	for name, r := range custom {
		if r != nil {
			m.rules[name] = r
		}
	}
	return m
}

// Mask applies the named rule. An unknown rule or nil value passes through
// unchanged; this is intentional so a bad config never breaks reads.
func (m *RuleMasker) Mask(rule string, value any) (any, error) {
	if rule == "" || value == nil {
		return value, nil
	}
	fn, ok := m.rules[rule]
	if !ok {
		return value, nil
	}
	return fn(value)
}

func builtins() map[string]Rule {
	return map[string]Rule{
		"email":  maskEmail,
		"phone":  maskPhone,
		"idcard": maskIDCard,
		"secret": maskSecret,
	}
}

func asString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	case fmt.Stringer:
		return s.String(), true
	}
	return "", false
}

func maskEmail(v any) (any, error) {
	s, ok := asString(v)
	if !ok || s == "" {
		return v, nil
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return v, nil
	}
	local := s[:at]
	domain := s[at:]
	first := local[0]
	return string(first) + "***" + domain, nil
}

func maskPhone(v any) (any, error) {
	s, ok := asString(v)
	if !ok || len(s) < 8 {
		return v, nil
	}
	return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:], nil
}

func maskIDCard(v any) (any, error) {
	s, ok := asString(v)
	if !ok || len(s) < 8 {
		return v, nil
	}
	return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:], nil
}

func maskSecret(v any) (any, error) {
	return "***", nil
}
