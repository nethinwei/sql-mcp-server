package workload

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

// TestArtifactsMatchCheckedInFiles locks the checked-in fixtures/v4 files to
// the generator: regenerate with `make fixtures-v4` after generator changes.
func TestArtifactsMatchCheckedInFiles(t *testing.T) {
	for path, want := range Artifacts(DefaultConfig()) {
		got, err := os.ReadFile(filepath.Join("..", path))
		if err != nil {
			t.Fatalf("checked-in artifact missing (run `make fixtures-v4`): %v", err)
		}
		if string(got) != want {
			t.Fatalf("artifact %s drifted from generator output (run `make fixtures-v4`)", path)
		}
	}
}

// TestProfileValidates loads the generated combined profile through the real
// config loader, so entity, relationship, mask, and budget declarations stay
// consistent with the configuration contract.
func TestProfileValidates(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://placeholder/db")
	cfg, err := configyaml.Load(filepath.Join("..", "profiles", "default.yaml"))
	if err != nil {
		t.Fatalf("profile does not validate: %v", err)
	}
	if len(cfg.Entities) != len(Generate(DefaultConfig()).Tables) {
		t.Fatalf("profile exposes %d entities, dataset has %d tables",
			len(cfg.Entities), len(Generate(DefaultConfig()).Tables))
	}
}

// TestEveryExpectedTaskHasCSV keeps task expectations and emitted CSVs in
// one-to-one correspondence.
func TestEveryExpectedTaskHasCSV(t *testing.T) {
	artifacts := Artifacts(DefaultConfig())
	for _, id := range ExpectedTaskIDs(DefaultConfig()) {
		found := false
		for path := range artifacts {
			if filepath.Base(path) == id+".csv" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("task %s has no expected CSV artifact", id)
		}
	}
}
