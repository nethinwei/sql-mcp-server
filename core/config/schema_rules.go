package config

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/mask"
)

// Loading runs three stages: strict decoding of the raw input (unknown keys
// fail), ApplyDefaults, then Validate. Validate checks the `schema` tags
// below last; they express unconditional single-field rules only. Rules that
// depend on other fields or on a switch (softScore >= hardScore, a path when
// audit is on, positive entry limits when the result cache is on) are named
// checks in Validate. The console checks the tag rules as hints through the
// generated schema; the server is authoritative. testdata/rules.json holds
// cases both sides must agree on.
//
// Field rules live in `schema` struct tags so that validation, the generated
// JSON Schema (schema.json), the console and the reference docs share one
// source. Tag grammar, comma separated:
//
//	required      the value must be set (non-zero)
//	min=N, max=N  numeric bounds; durations use Go syntax (max=5s)
//	minLength=N   strings
//	minItems=N    slices
//	enum=@name    one of a named enumeration (see enums)
//	pattern=@name matches a named pattern (see patterns)
//	keys=@name    map keys match a named pattern
//	restart       a change applies only after a restart (see RestartFieldChanges)
//	nodefault     the generated schema shows no defaults below this field
//
// On a slice, enum, pattern and minLength apply to its elements. An empty
// string skips enum, pattern and minLength unless the field is required. A
// struct that is entirely unset and not required is not checked, matching an
// absent object in the schema.

var enums = map[string][]string{
	"configVersion":     {"1"},
	"transport":         {"stdio", "http"},
	"entityKind":        {"table", "view", "procedure"},
	"grantAction":       entity.ActionNames(),
	"cardinality":       entity.Cardinalities,
	"connectionRole":    {"primary", "replica"},
	"pooler":            {"transaction"},
	"sqlEscapeEstimate": {SQLEscapeEstimateRequired, SQLEscapeEstimateSkip},
	"sqlEscapeMetadata": {SQLEscapeMetadataAllow, SQLEscapeMetadataRefuse},
	"clientIdentity":    {ClientIdentityUser, ClientIdentityDefault},
	// Mask rules are the masker's; an unknown one would fail every startup.
	"maskRule": mask.BuiltinRules(),
}

var patterns = map[string]*regexp.Regexp{
	"accessName":  regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`),
	"driver":      regexp.MustCompile(`^[a-z][a-z0-9_-]*$`),
	"tokenHash":   regexp.MustCompile(`^sha256:[0-9a-f]{64}$`),
	"certSubject": regexp.MustCompile(`^(?i:cn|dns|uri|email):\S.*$`),
	"pathPrefix":  regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`),
	"httpURL":     regexp.MustCompile(`^https?://[^\s/?#]+(/[^\s?#]*)?$`),
	// An entity is named datasource.schema.name, so none of them has a dot.
	"pathSegment": regexp.MustCompile(`^[^.]+$`),
}

// Rule is the parsed `schema` tag of one field.
type Rule struct {
	Required, Restart, NoDefault bool
	// Min and Max are numbers, or Go durations on duration fields.
	Min, Max            string
	MinLength, MinItems int
	Enum                []string
	// Pattern and Keys are regular expressions; the names are their keys in
	// the named pattern table.
	Pattern, Keys         string
	PatternName, KeysName string
}

// ParseRule parses a `schema` struct tag.
func ParseRule(tag string) (Rule, error) {
	var r Rule
	if tag == "" {
		return r, nil
	}
	for _, part := range strings.Split(tag, ",") {
		key, val, _ := strings.Cut(part, "=")
		var err error
		switch key {
		case "required":
			r.Required = true
		case "restart":
			r.Restart = true
		case "nodefault":
			r.NoDefault = true
		case "min":
			r.Min = val
		case "max":
			r.Max = val
		case "minLength":
			r.MinLength, err = strconv.Atoi(val)
		case "minItems":
			r.MinItems, err = strconv.Atoi(val)
		case "enum":
			r.Enum, err = namedEnum(val)
		case "pattern":
			r.PatternName, r.Pattern, err = namedPattern(val)
		case "keys":
			r.KeysName, r.Keys, err = namedPattern(val)
		default:
			err = fmt.Errorf("unknown key %q", key)
		}
		if err != nil {
			return Rule{}, fmt.Errorf("schema tag %q: %w", tag, err)
		}
	}
	return r, nil
}

func namedEnum(ref string) ([]string, error) {
	values, ok := enums[strings.TrimPrefix(ref, "@")]
	if !strings.HasPrefix(ref, "@") || !ok {
		return nil, fmt.Errorf("unknown enum %q", ref)
	}
	return slices.Clone(values), nil
}

func namedPattern(ref string) (string, string, error) {
	name := strings.TrimPrefix(ref, "@")
	re, ok := patterns[name]
	if !strings.HasPrefix(ref, "@") || !ok {
		return "", "", fmt.Errorf("unknown pattern %q", ref)
	}
	return name, re.String(), nil
}

// YAMLName returns a field's configuration key, or "" when it has none.
func YAMLName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if name == "-" {
		return ""
	}
	return name
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// validateRules enforces the `schema` tags of every set field.
func (c *Config) validateRules() error {
	return checkStruct(reflect.ValueOf(c).Elem(), "")
}

// fieldRule is a field of a struct type with the rule its tag says.
type fieldRule struct {
	index int
	name  string
	rule  Rule
	err   error
}

// structRules caches the rules of each struct type, by reflect.Type: a
// configuration of thousands of entities checks each type's fields
// thousands of times.
var structRules sync.Map

func rulesOf(t reflect.Type) []fieldRule {
	if rules, ok := structRules.Load(t); ok {
		return rules.([]fieldRule)
	}
	var rules []fieldRule
	for i := range t.NumField() {
		name := YAMLName(t.Field(i))
		if name == "" {
			continue
		}
		rule, err := ParseRule(t.Field(i).Tag.Get("schema"))
		rules = append(rules, fieldRule{index: i, name: name, rule: rule, err: err})
	}
	structRules.Store(t, rules)
	return rules
}

// compiledPatterns caches the patterns of rules, compiled.
var compiledPatterns sync.Map

func compiledPattern(pattern string) *regexp.Regexp {
	if re, ok := compiledPatterns.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(pattern)
	compiledPatterns.Store(pattern, re)
	return re
}

// checkStruct checks the fields of v, at path. Their paths are joined only
// to report an error or to go into a nested value: most values are scalars
// that pass.
func checkStruct(v reflect.Value, path string) error {
	for _, f := range rulesOf(v.Type()) {
		if f.err != nil {
			return fmt.Errorf("config: %s: %w", joinPath(path, f.name), f.err)
		}
		if err := checkValue(v.Field(f.index), f.rule, path, f.name); err != nil {
			return err
		}
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

// checkValue checks v, the value named name under parent (an element of a
// list is named by its index: see elementName).
func checkValue(v reflect.Value, r Rule, parent, name string) (err error) {
	if r.Required && v.IsZero() {
		return fmt.Errorf("config: %s is required", joinPath(parent, name))
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		inner := r
		inner.Required = false
		return checkValue(v.Elem(), inner, parent, name)
	case reflect.Struct:
		if v.IsZero() {
			return nil
		}
		return checkStruct(v, joinPath(parent, name))
	case reflect.Slice:
		path := joinPath(parent, name)
		if v.Len() < r.MinItems {
			return fmt.Errorf("config: %s needs at least %d items", path, r.MinItems)
		}
		item := Rule{Enum: r.Enum, Pattern: r.Pattern, MinLength: r.MinLength}
		for i := range v.Len() {
			// Elements are present even when zero, unlike an unset struct field;
			// an empty string names nothing (an empty role, action, subject).
			if elem := v.Index(i); elem.Kind() == reflect.Struct {
				err = checkStruct(elem, elementName(path, i))
			} else if elem.Kind() == reflect.String && elem.String() == "" {
				err = fmt.Errorf("config: %s must not be empty", elementName(path, i))
			} else {
				err = checkValue(elem, item, "", elementName(path, i))
			}
			if err != nil {
				return err
			}
		}
	case reflect.Map:
		return checkMap(v, r, joinPath(parent, name))
	default:
		return checkScalar(v, r, parent, name)
	}
	return nil
}

func elementName(path string, i int) string { return path + "[" + strconv.Itoa(i) + "]" }

func checkMap(v reflect.Value, r Rule, path string) error {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		if r.Keys != "" && !compiledPattern(r.Keys).MatchString(k.String()) {
			return fmt.Errorf("config: %s key %q must match %s", path, k.String(), r.Keys)
		}
		if elem := v.MapIndex(k); elem.Kind() == reflect.Struct {
			if err := checkStruct(elem, joinPath(path, k.String())); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkScalar(v reflect.Value, r Rule, parent, name string) error {
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if s == "" && !r.Required {
			return nil
		}
		if len(s) < r.MinLength {
			return fmt.Errorf("config: %s must have at least %d characters", joinPath(parent, name), r.MinLength)
		}
		if r.Enum != nil && !slices.Contains(r.Enum, s) {
			return fmt.Errorf("config: %s is %q, want one of %s", joinPath(parent, name), s, strings.Join(r.Enum, ", "))
		}
		if r.Pattern != "" && !compiledPattern(r.Pattern).MatchString(s) {
			return fmt.Errorf("config: %s %q must match %s", joinPath(parent, name), s, r.Pattern)
		}
	case reflect.Int, reflect.Int64, reflect.Float64:
		if r.Min == "" && r.Max == "" {
			return nil
		}
		return checkBounds(v, r, joinPath(parent, name))
	}
	return nil
}

func checkBounds(v reflect.Value, r Rule, path string) error {
	if r.Min == "" && r.Max == "" {
		return nil
	}
	n, lo, hi, err := bounds(v, r)
	if err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	switch {
	case math.IsNaN(n):
		return fmt.Errorf("config: %s must be a number", path)
	case r.Min != "" && n < lo:
		return fmt.Errorf("config: %s must be at least %s", path, r.Min)
	case r.Max != "" && n > hi:
		return fmt.Errorf("config: %s must be at most %s", path, r.Max)
	}
	return nil
}

// bounds returns the value and its bounds as numbers; durations compare in
// nanoseconds.
func bounds(v reflect.Value, r Rule) (n, lo, hi float64, err error) {
	parse := func(s string) (float64, error) {
		if s == "" {
			return 0, nil
		}
		if v.Type() == durationType {
			d, err := time.ParseDuration(s)
			return float64(d), err
		}
		return strconv.ParseFloat(s, 64)
	}
	if v.Kind() == reflect.Float64 {
		n = v.Float()
	} else {
		n = float64(v.Int())
	}
	if lo, err = parse(r.Min); err != nil {
		return 0, 0, 0, err
	}
	hi, err = parse(r.Max)
	return n, lo, hi, err
}

// RestartFieldChanges returns the paths of fields tagged restart whose value
// differs between old and next. Empty and unset lists or maps compare equal,
// and unexported bookkeeping is ignored.
func RestartFieldChanges(old, next *Config) []string {
	var out []string
	collectRestart(reflect.ValueOf(old).Elem(), reflect.ValueOf(next).Elem(), "", &out)
	return out
}

func collectRestart(a, b reflect.Value, path string, out *[]string) {
	t := a.Type()
	for i := range t.NumField() {
		name := YAMLName(t.Field(i))
		if name == "" {
			continue
		}
		rule, _ := ParseRule(t.Field(i).Tag.Get("schema"))
		p := joinPath(path, name)
		switch {
		case rule.Restart:
			if !sameValue(a.Field(i), b.Field(i)) {
				*out = append(*out, p)
			}
		case a.Field(i).Kind() == reflect.Struct:
			collectRestart(a.Field(i), b.Field(i), p, out)
		}
	}
}

func sameValue(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return sameValue(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := range a.NumField() {
			if a.Type().Field(i).IsExported() && !sameValue(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !sameValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}
		for _, k := range a.MapKeys() {
			if bv := b.MapIndex(k); !bv.IsValid() || !sameValue(a.MapIndex(k), bv) {
				return false
			}
		}
		return true
	}
	return a.Interface() == b.Interface()
}
