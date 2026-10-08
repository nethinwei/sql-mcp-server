package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

// schema.json and the field reference must match the structs and fields.yaml.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	schema, doc, err := outputs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{schemaPath: schema, docPath: doc} {
		got, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run `go generate ./core/config`", path)
		}
	}
}

func TestGenerateReportsDocDrift(t *testing.T) {
	fields, err := os.ReadFile(filepath.Join(repoRoot, fieldsPath))
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(fields), "cost.maxRows:", "cost.maxRow:", 1)
	_, _, err = Generate([]byte(broken))
	if err == nil || !strings.Contains(err.Error(), "missing doc cost.maxRows") ||
		!strings.Contains(err.Error(), "unused doc cost.maxRow") {
		t.Fatalf("err = %v", err)
	}
}
