package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"gopkg.in/yaml.v3"
)

// Doc is the help text of one configuration path in core/config/fields.yaml.
type Doc struct {
	Title map[string]string `yaml:"title"`
	ZH    string            `yaml:"zh-CN"`
	EN    string            `yaml:"en"`
}

// defNames lists the types emitted once under $defs and referenced elsewhere;
// their help text is keyed "defs.<name>[.<field>...]".
var defNames = map[reflect.Type]string{
	reflect.TypeFor[config.DatabaseConfig](): "database",
	reflect.TypeFor[config.EntityConfig]():   "entity",
	reflect.TypeFor[config.RoleDefinition](): "role",
	reflect.TypeFor[config.GrantConfig]():    "grant",
	reflect.TypeFor[config.UserConfig]():     "user",
	reflect.TypeFor[config.BudgetLimits]():   "budgetLimits",
}

var defOrder = []string{"database", "entity", "role", "grant", "user", "budgetLimits"}

// durationPattern accepts what time.ParseDuration accepts.
const durationPattern = `^(0|-?([0-9]+(\.[0-9]*)?(ns|us|µs|ms|s|m|h))+)$`

var durationType = reflect.TypeFor[time.Duration]()

// pair holds a field of the seed before and after ApplyDefaults; either may
// be invalid where the seed has no value.
type pair struct{ before, after reflect.Value }

func (p pair) field(i int) pair {
	return pair{fieldOf(p.before, i), fieldOf(p.after, i)}
}

func fieldOf(v reflect.Value, i int) reflect.Value {
	if !v.IsValid() {
		return v
	}
	return v.Field(i)
}

func (p pair) elem() pair {
	return pair{elemOf(p.before), elemOf(p.after)}
}

// elemOf returns the pointee, the first list element or the first map value.
func elemOf(v reflect.Value) reflect.Value {
	switch {
	case !v.IsValid():
		return v
	case v.Kind() == reflect.Pointer:
		if v.IsNil() {
			return reflect.Value{}
		}
		return v.Elem()
	case v.Kind() == reflect.Slice && v.Len() > 0:
		return v.Index(0)
	case v.Kind() == reflect.Map && v.Len() > 0:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		return v.MapIndex(keys[0])
	}
	return reflect.Value{}
}

// node is one schema object. Keys are written in a fixed order.
type node map[string]any

var keyOrder = []string{
	"$schema", "$ref", "title", "x-title-zh-CN", "description", "x-description-zh-CN", "type", "x-format",
	"x-minimum", "x-maximum", "default", "enum", "examples", "pattern", "minLength", "minimum", "maximum", "minItems",
	"x-restart", "anyOf",
	"required", "properties", "propertyNames", "additionalProperties", "items", "$defs",
}

func rank(key string) int {
	for i, k := range keyOrder {
		if k == key {
			return i
		}
	}
	return len(keyOrder)
}

// MarshalJSON writes keys in keyOrder; nested property maps keep the order
// recorded in their "x-order" pseudo key.
func (n node) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(n))
	for k := range n {
		if k != "x-order" {
			keys = append(keys, k)
		}
	}
	if order, ok := n["x-order"].([]string); ok {
		keys = order
	} else {
		sort.SliceStable(keys, func(i, j int) bool { return rank(keys[i]) < rank(keys[j]) })
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(n[k])
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// field is one row of the generated reference.
type field struct {
	Path, Type, Default, Constraints string
	Doc                              Doc
	Restart                          bool
}

type generator struct {
	docs    map[string]Doc
	used    map[string]bool
	defs    map[string]node
	rows    map[string][]field // by section ("" for defs: "defs.<name>")
	missing []string
}

// Generate returns schema.json and the markdown field reference.
func Generate(fieldsYAML []byte) (schema []byte, reference string, err error) {
	g := &generator{used: map[string]bool{}, defs: map[string]node{}, rows: map[string][]field{}}
	if err := yaml.Unmarshal(fieldsYAML, &g.docs); err != nil {
		return nil, "", fmt.Errorf("fields.yaml: %w", err)
	}
	before, after := seed(), seed()
	after.ApplyDefaults()
	values := pair{reflect.ValueOf(before), reflect.ValueOf(after)}
	root := g.object(reflect.TypeFor[config.Config](), "", "", values, false)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = "sql-mcp-server configuration"
	// Validate requires one of them (see Config.resolvedDatabases).
	root["anyOf"] = []node{{"required": []string{"database"}}, {"required": []string{"databases"}}}
	defs := node{"x-order": defOrder}
	for _, name := range defOrder {
		defs[name] = g.defs[name]
	}
	root["$defs"] = defs
	for key := range g.docs {
		if !g.used[key] {
			g.missing = append(g.missing, "unused doc "+key)
		}
	}
	if len(g.missing) > 0 {
		sort.Strings(g.missing)
		return nil, "", fmt.Errorf("fields.yaml out of step with core/config:\n  %s", strings.Join(g.missing, "\n  "))
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return append(out, '\n'), g.reference(), nil
}

// seed returns a configuration with every optional block switched on and one
// element in each list or map, so ApplyDefaults reaches every field. The
// schema reports as defaults exactly the values ApplyDefaults changes.
func seed() config.Config {
	return config.Config{
		Entities: []config.EntityConfig{{Name: "x", Fields: []config.FieldConfig{{Name: "x"}},
			Relationships: []config.RelationshipConfig{{}}}},
		Roles: map[string]config.RoleDefinition{"x": {Grants: []config.GrantConfig{{}}}},
		Users: map[string]config.UserConfig{"x": {}},
		Budget: config.BudgetConfig{Roles: map[string]config.BudgetLimits{"x": {}},
			Tenants: map[string]config.BudgetLimits{"x": {}}, Users: map[string]config.BudgetLimits{"x": {}}},
		Cache: config.CacheConfig{Enabled: true},
	}
}

func (g *generator) doc(key string, n node) Doc {
	d, ok := g.docs[key]
	g.used[key] = true
	if !ok || d.ZH == "" || d.EN == "" {
		g.missing = append(g.missing, "missing doc "+key)
		return d
	}
	n["description"] = d.EN
	n["x-description-zh-CN"] = d.ZH
	if d.Title != nil {
		n["title"] = d.Title["en"]
		n["x-title-zh-CN"] = d.Title["zh-CN"]
	}
	return d
}

// object returns the schema of a struct type. key is the doc key prefix,
// section the reference table the fields go to.
func (g *generator) object(t reflect.Type, key, section string, val pair, noDefault bool) node {
	props := node{}
	var order, required []string
	for i := range t.NumField() {
		f := t.Field(i)
		name := config.YAMLName(f)
		if name == "" {
			continue
		}
		rule, err := config.ParseRule(f.Tag.Get("schema"))
		if err != nil {
			g.missing = append(g.missing, err.Error())
			continue
		}
		fv := val.field(i)
		fkey := join(key, name)
		sec := section
		if sec == "" {
			sec = name // a top-level field starts its own section
		}
		props[name] = g.property(f.Type, rule, fkey, sec, fv, noDefault || rule.NoDefault)
		order = append(order, name)
		if rule.Required {
			required = append(required, name)
		}
	}
	props["x-order"] = order
	n := node{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		n["required"] = required
	}
	return n
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

// property returns the schema of one field and records its reference row.
func (g *generator) property(t reflect.Type, rule config.Rule, key, section string, val pair, noDefault bool) node {
	n := g.value(t, rule, key, section, val, noDefault)
	d := g.doc(key, n)
	if rule.Restart {
		n["x-restart"] = true
	}
	path := key
	if strings.HasPrefix(key, "defs.") {
		path = strings.SplitN(key, ".", 3)[2]
	} else if rest, ok := strings.CutPrefix(key, section+"."); ok {
		path = rest
	}
	g.rows[section] = append(g.rows[section], field{
		Path: path, Type: typeLabel(n), Default: defaultLabel(n), Constraints: constraints(n, rule),
		Doc: d, Restart: rule.Restart,
	})
	return n
}

// value returns the schema of a type at key; list and map elements share the
// key of their container.
func (g *generator) value(t reflect.Type, rule config.Rule, key, section string, val pair, noDefault bool) node {
	if t.Kind() == reflect.Pointer {
		val, t = val.elem(), t.Elem()
	}
	n := node{}
	switch {
	case t == durationType:
		n["type"], n["x-format"], n["pattern"] = "string", "duration", durationPattern
	case t.Kind() == reflect.Struct:
		if name, ok := defNames[t]; ok {
			g.def(t, name, val, noDefault)
			return node{"$ref": "#/$defs/" + name}
		}
		return g.object(t, key, section, val, noDefault)
	case t.Kind() == reflect.Slice:
		n["type"] = "array"
		item := config.Rule{Enum: rule.Enum, Pattern: rule.Pattern, MinLength: rule.MinLength}
		n["items"] = g.value(t.Elem(), item, key, section, val.elem(), noDefault)
		if rule.MinItems > 0 {
			n["minItems"] = rule.MinItems
		}
		return withDefault(n, val, noDefault)
	case t.Kind() == reflect.Map:
		n["type"] = "object"
		if rule.Keys != "" {
			n["propertyNames"] = node{"pattern": rule.Keys}
		}
		if t.Elem().Kind() != reflect.Interface {
			n["additionalProperties"] = g.value(t.Elem(), config.Rule{}, key, section, val.elem(), noDefault)
		}
		return n
	case t.Kind() == reflect.String:
		n["type"] = "string"
	case t.Kind() == reflect.Bool:
		n["type"] = "boolean"
	case t.Kind() == reflect.Float64:
		n["type"] = "number"
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		n["type"] = "integer"
	case t.Kind() == reflect.Interface:
		return n
	}
	scalarRules(n, rule)
	return withDefault(n, val, noDefault)
}

func scalarRules(n node, rule config.Rule) {
	if rule.Enum != nil {
		n["enum"] = rule.Enum
	}
	if rule.Examples != nil {
		n["examples"] = rule.Examples
	}
	if rule.Pattern != "" {
		n["pattern"] = rule.Pattern
	}
	if rule.MinLength > 0 {
		n["minLength"] = rule.MinLength
	}
	if n["x-format"] == "duration" {
		// Duration bounds stay in Go syntax; the schema keeps them as hints.
		if rule.Min != "" {
			n["x-minimum"] = rule.Min
		}
		if rule.Max != "" {
			n["x-maximum"] = rule.Max
		}
		return
	}
	if rule.Min != "" {
		n["minimum"] = number(rule.Min)
	}
	if rule.Max != "" {
		n["maximum"] = number(rule.Max)
	}
}

func number(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// withDefault records the value ApplyDefaults gave the field, if it set one.
func withDefault(n node, val pair, noDefault bool) node {
	v := val.after
	if noDefault || !v.IsValid() || v.IsZero() ||
		val.before.IsValid() && reflect.DeepEqual(val.before.Interface(), v.Interface()) {
		return n
	}
	if v.Type() == durationType {
		n["default"] = shortDuration(time.Duration(v.Int()))
		return n
	}
	n["default"] = v.Interface()
	return n
}

// shortDuration drops zero trailing units: 5m rather than 5m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (g *generator) def(t reflect.Type, name string, val pair, noDefault bool) {
	if _, done := g.defs[name]; done {
		return
	}
	g.defs[name] = node{} // guards recursion
	key := "defs." + name
	n := g.object(t, key, key, val, noDefault)
	g.doc(key, n)
	g.defs[name] = n
}
