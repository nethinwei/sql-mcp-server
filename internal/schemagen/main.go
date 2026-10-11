// Command schemagen generates core/config/schema.json, the field reference
// in docs/configuration.md and the Specs (core/config/spec_gen.go) from the
// configuration structs (their `schema` tags and ApplyDefaults) and the help
// text in core/config/fields.yaml.
// Run it with `go generate ./core/config`; tests fail when the outputs are
// stale.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	beginMarker = "<!-- BEGIN GENERATED: config-fields -->"
	endMarker   = "<!-- END GENERATED: config-fields -->"
)

// Paths relative to the repository root.
const (
	fieldsPath = "core/config/fields.yaml"
	schemaPath = "core/config/schema.json"
	docPath    = "docs/configuration.md"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := write(*root); err != nil {
		fmt.Fprintln(os.Stderr, "schemagen:", err)
		os.Exit(1)
	}
}

// outputs returns the generated files by path: the schema, the
// documentation with its generated section replaced, and the Specs.
func outputs(root string) (map[string][]byte, error) {
	fields, err := os.ReadFile(filepath.Join(root, fieldsPath))
	if err != nil {
		return nil, err
	}
	schema, reference, err := Generate(fields)
	if err != nil {
		return nil, err
	}
	current, err := os.ReadFile(filepath.Join(root, docPath))
	if err != nil {
		return nil, err
	}
	doc, err := replaceSection(string(current), reference)
	if err != nil {
		return nil, err
	}
	spec, err := GenerateSpec()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{schemaPath: schema, docPath: doc, specPath: spec}, nil
}

func replaceSection(doc, body string) ([]byte, error) {
	begin := strings.Index(doc, beginMarker)
	end := strings.Index(doc, endMarker)
	if begin < 0 || end < begin {
		return nil, errors.New(docPath + " lacks the generated-section markers")
	}
	return []byte(doc[:begin+len(beginMarker)] + "\n" + body + "\n" + doc[end:]), nil
}

func write(root string) error {
	files, err := outputs(root)
	if err != nil {
		return err
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
