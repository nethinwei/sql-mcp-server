package mask

import (
	"fmt"
	"sort"
	"strings"
)

// redacted replaces any value a rule cannot mask by format, so a malformed or
// unexpected value is never returned in plaintext.
const redacted = "***"

// Masker applies a named masking rule to a value. Implementations must not
// panic on unknown rules or nil values.
type Masker interface {
	Mask(rule string, value any) any
}

// NoopMasker passes values through unchanged; used when masking is disabled.
type NoopMasker struct{}

// Mask implements Masker by returning the value unchanged.
func (NoopMasker) Mask(_ string, value any) any { return value }

// Rule masks a single non-nil value.
type Rule func(value any) any

// RuleMasker maps rule names to Rules. The zero value masks nothing.
type RuleMasker struct {
	rules map[string]Rule
}

// NewRuleMasker returns a Masker with the built-in rules (email, phone, idcard,
// secret).
func NewRuleMasker() *RuleMasker {
	return &RuleMasker{rules: builtins()}
}

// Mask applies the named rule. An unknown rule or nil value passes through
// unchanged; assemblers reject unknown rule names at startup (see Has).
func (m *RuleMasker) Mask(rule string, value any) any {
	if rule == "" || value == nil {
		return value
	}
	fn, ok := m.rules[rule]
	if !ok {
		return value
	}
	return fn(value)
}

// Has reports whether a rule name is registered. Assemblers call it to reject a
// misconfigured mask rule at startup rather than silently leaking plaintext at
// read time.
func (m *RuleMasker) Has(name string) bool {
	_, ok := m.rules[name]
	return ok
}

// BuiltinRules returns the names of the built-in mask rules, sorted.
func BuiltinRules() []string {
	names := make([]string, 0, len(builtins()))
	for name := range builtins() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func builtins() map[string]Rule {
	return map[string]Rule{
		"email":  maskEmail,
		"phone":  maskDigits,
		"idcard": maskDigits,
		"secret": func(any) any { return redacted },
	}
}

// coerceString renders strings and scalars — including numeric IDs stored as
// int/float — to a string, so a phone or ID card held as a number is masked.
func coerceString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	case fmt.Stringer:
		return s.String(), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(s), true
	}
	return "", false
}

func maskEmail(v any) any {
	s, ok := coerceString(v)
	if ok && s == "" {
		return s
	}
	at := strings.IndexByte(s, '@')
	if !ok || at <= 0 {
		return redacted
	}
	return s[:1] + redacted + s[at:]
}

// maskDigits keeps the first three and last four characters of a phone or ID
// card number.
func maskDigits(v any) any {
	s, ok := coerceString(v)
	if !ok || len(s) < 8 {
		return redacted
	}
	return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:]
}
