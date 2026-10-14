package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// exampleOmits are the schema fields examples/config.example.yaml leaves
// out on purpose, with why; a field under one is left out too.
var exampleOmits = map[string]string{
	"database": "the single-datasource shorthand of databases; a configuration sets one of the two",
}

// schemaNode is the part of a JSON Schema node the example check walks.
type schemaNode struct {
	Ref                  string                 `json:"$ref"`
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	AdditionalProperties json.RawMessage        `json:"additionalProperties"`
}

// values is the schema of a map's values, when the node is a map.
func (n *schemaNode) values() *schemaNode {
	var v schemaNode
	if len(n.AdditionalProperties) == 0 || json.Unmarshal(n.AdditionalProperties, &v) != nil {
		return nil // false, or absent: no map
	}
	return &v
}

// exampleCheck compares the fields the example sets with the schema's.
type exampleCheck struct {
	defs     map[string]*schemaNode
	set      map[string]bool // fields the example sets
	problems []string
}

// checkExampleCoversSchema reports every configuration field (in
// core/config/schema.json) that examples/config.example.yaml, the complete
// template, does not set: a new field must be shown there.
func checkExampleCoversSchema(root string) []string {
	var schema struct {
		schemaNode
		Defs map[string]*schemaNode `json:"$defs"`
	}
	var example any
	data, err := os.ReadFile(filepath.Join(root, "core", "config", "schema.json"))
	if err == nil {
		err = json.Unmarshal(data, &schema)
	}
	if err == nil {
		data, err = os.ReadFile(filepath.Join(root, "examples", "config.example.yaml"))
	}
	if err == nil {
		err = yaml.Unmarshal(data, &example)
	}
	if err != nil {
		return []string{fmt.Sprintf("example coverage: %v", err)}
	}
	c := &exampleCheck{defs: schema.Defs, set: map[string]bool{}}
	c.walk(example, &schema.schemaNode, "")
	c.fields(&schema.schemaNode, "", map[string]bool{})
	return c.problems
}

func (c *exampleCheck) resolve(n *schemaNode) *schemaNode {
	for n != nil && n.Ref != "" {
		n = c.defs[strings.TrimPrefix(n.Ref, "#/$defs/")]
	}
	return n
}

// walk records the fields value sets, n being its schema.
func (c *exampleCheck) walk(value any, n *schemaNode, path string) {
	if n = c.resolve(n); n == nil {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for k, x := range v {
			if p, ok := n.Properties[k]; ok {
				c.set[join(path, k)] = true
				c.walk(x, p, join(path, k))
			} else if values := n.values(); values != nil {
				c.walk(x, values, join(path, "*"))
			}
		}
	case []any:
		for _, x := range v {
			c.walk(x, n.Items, join(path, "[]"))
		}
	}
}

// fields reports the fields of n the example does not set; seen holds the
// definitions on the way, a definition referring to itself ending the walk.
func (c *exampleCheck) fields(n *schemaNode, path string, seen map[string]bool) {
	if n.Ref != "" {
		if seen[n.Ref] {
			return
		}
		seen = maps.Clone(seen)
		seen[n.Ref] = true
	}
	if n = c.resolve(n); n == nil {
		return
	}
	for k, p := range n.Properties {
		field := join(path, k)
		switch {
		case omitted(field):
		case !c.set[field]:
			c.problems = append(c.problems, fmt.Sprintf(
				"examples/config.example.yaml: field %s is not shown (or add it to exampleOmits)", field))
		default:
			c.fields(p, field, seen)
		}
	}
	if n.Items != nil {
		c.fields(n.Items, join(path, "[]"), seen)
	}
	if values := n.values(); values != nil {
		c.fields(values, join(path, "*"), seen)
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func omitted(field string) bool {
	for prefix := range exampleOmits {
		if field == prefix || strings.HasPrefix(field, prefix+".") {
			return true
		}
	}
	return false
}
