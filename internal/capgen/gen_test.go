package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	_ "github.com/nethinwei/sql-mcp-server/x/providers/all"
)

const repoRoot = "../.."

func TestGeneratedSectionIsCurrent(t *testing.T) {
	want, err := output(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(repoRoot, docPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale; run `go generate ./x/providers/all`", docPath)
	}
}

// Every built-in driver has its table: a new provider cannot ship without
// stating what it guarantees.
func TestEveryDriverIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range providers {
		listed[p.driver] = true
	}
	for _, driver := range providerregistry.Drivers() {
		if !listed[driver] {
			t.Errorf("driver %q has no capability table in internal/capgen", driver)
		}
	}
}
