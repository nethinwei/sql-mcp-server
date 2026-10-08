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
	"time"

	"github.com/nethinwei/sql-mcp-server/core/mask"
)

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
//	examples=@name suggested values (see suggestions); never enforced
//	restart       a change applies only after a restart (see RestartFieldChanges)
//	nodefault     the generated schema shows no defaults below this field
//
// On a slice, enum, pattern and minLength apply to its elements. An empty
// string skips enum, pattern and minLength unless the field is required. A
// struct that is entirely unset and not required is not checked, matching an
// absent object in the schema.

var enums = map[string][]string{
	"transport":   {"stdio", "http"},
	"entityKind":  {"table", "view", "procedure"},
	"grantAction": {"read", "create", "update", "delete", "execute", "aggregate"},
	"cardinality": {"one", "one-to-one", "belongs-to", "many", "one-to-many", "has-many"},
}

// suggestions are offered to editors but not enforced: extensions may add
// mask rules, which x/bootstrap checks against the configured masker.
var suggestions = map[string]func() []string{
	"maskRule": mask.BuiltinRules,
}

var patterns = map[string]*regexp.Regexp{
	"accessName": regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`),
	"driver":     regexp.MustCompile(`^[a-z][a-z0-9_-]*$`),
	"tokenHash":  regexp.MustCompile(`^sha256:[0-9a-f]{64}$`),
}

// Rule is the parsed `schema` tag of one field.
type Rule struct {
	Required, Restart, NoDefault bool
	// Min and Max are numbers, or Go durations on duration fields.
	Min, Max            string
	MinLength, MinItems int
	Enum                []string
	Examples            []string
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
		case "examples":
			r.Examples, err = namedSuggestion(val)
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

func namedSuggestion(ref string) ([]string, error) {
	values, ok := suggestions[strings.TrimPrefix(ref, "@")]
	if !strings.HasPrefix(ref, "@") || !ok {
		return nil, fmt.Errorf("unknown suggestion list %q", ref)
	}
	return values(), nil
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

func checkStruct(v reflect.Value, path string) error {
	t := v.Type()
	for i := range t.NumField() {
		name := YAMLName(t.Field(i))
		if name == "" {
			continue
		}
		rule, err := ParseRule(t.Field(i).Tag.Get("schema"))
		if err != nil {
			return fmt.Errorf("config: %s: %w", joinPath(path, name), err)
		}
		if err := checkValue(v.Field(i), rule, joinPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

func checkValue(v reflect.Value, r Rule, path string) (err error) {
	if r.Required && v.IsZero() {
		return fmt.Errorf("config: %s is required", path)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		inner := r
		inner.Required = false
		return checkValue(v.Elem(), inner, path)
	case reflect.Struct:
		if v.IsZero() {
			return nil
		}
		return checkStruct(v, path)
	case reflect.Slice:
		if v.Len() < r.MinItems {
			return fmt.Errorf("config: %s needs at least %d items", path, r.MinItems)
		}
		item := Rule{Enum: r.Enum, Pattern: r.Pattern, MinLength: r.MinLength}
		for i := range v.Len() {
			elem, p := v.Index(i), fmt.Sprintf("%s[%d]", path, i)
			// Elements are present even when zero, unlike an unset struct field.
			if elem.Kind() == reflect.Struct {
				err = checkStruct(elem, p)
			} else {
				err = checkValue(elem, item, p)
			}
			if err != nil {
				return err
			}
		}
	case reflect.Map:
		return checkMap(v, r, path)
	default:
		return checkScalar(v, r, path)
	}
	return nil
}

func checkMap(v reflect.Value, r Rule, path string) error {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		if r.Keys != "" && !regexp.MustCompile(r.Keys).MatchString(k.String()) {
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

func checkScalar(v reflect.Value, r Rule, path string) error {
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if s == "" && !r.Required {
			return nil
		}
		if len(s) < r.MinLength {
			return fmt.Errorf("config: %s must have at least %d characters", path, r.MinLength)
		}
		if r.Enum != nil && !slices.Contains(r.Enum, s) {
			return fmt.Errorf("config: %s is %q, want one of %s", path, s, strings.Join(r.Enum, ", "))
		}
		if r.Pattern != "" && !regexp.MustCompile(r.Pattern).MatchString(s) {
			return fmt.Errorf("config: %s %q must match %s", path, s, r.Pattern)
		}
	case reflect.Int, reflect.Int64, reflect.Float64:
		return checkBounds(v, r, path)
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
