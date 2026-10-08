// Command schemagen generates core/config/schema.json and the field
// reference in docs/configuration.md from the configuration structs (their
// `schema` tags and ApplyDefaults) and the help text in core/config/fields.yaml.
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

// outputs returns the generated schema and the documentation with its
// generated section replaced.
func outputs(root string) (schema, doc []byte, err error) {
	fields, err := os.ReadFile(filepath.Join(root, fieldsPath))
	if err != nil {
		return nil, nil, err
	}
	schema, reference, err := Generate(fields)
	if err != nil {
		return nil, nil, err
	}
	current, err := os.ReadFile(filepath.Join(root, docPath))
	if err != nil {
		return nil, nil, err
	}
	doc, err = replaceSection(string(current), reference)
	return schema, doc, err
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
	schema, doc, err := outputs(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, schemaPath), schema, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, docPath), doc, 0o644)
}
