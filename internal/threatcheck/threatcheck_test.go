// Package threatcheck machine-checks the traceability mapping from
// critical/high threat IDs (docs/threat-model.md) to regression tests.
// It runs in the default unit suite, so CI fails when a threat lacks
// evidence, a referenced test disappears, or a new TM ID is not mapped.
package threatcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type testRef struct {
	File string `json:"file"`
	Name string `json:"name"`
}

type coverage struct {
	Severity string    `json:"severity"`
	Tests    []testRef `json:"tests"`
	Gaps     []string  `json:"gaps"`
}

const repoRoot = "../.."

func loadCoverage(t *testing.T) map[string]coverage {
	t.Helper()
	data, err := os.ReadFile("coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]coverage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestEveryLedgerThreatIsMapped ensures every TM ID registered in the threat
// ledger has an entry in coverage.json, so adding TM-009 without mapping its
// evidence fails CI.
func TestEveryLedgerThreatIsMapped(t *testing.T) {
	ledger, err := os.ReadFile(filepath.Join(repoRoot, "docs", "threat-model.md"))
	if err != nil {
		t.Fatal(err)
	}
	mapped := loadCoverage(t)
	ids := regexp.MustCompile(`TM-\d{3}`).FindAllString(string(ledger), -1)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, ok := mapped[id]; !ok {
			t.Errorf("%s appears in docs/threat-model.md but has no entry in coverage.json", id)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no TM IDs found in docs/threat-model.md")
	}
	for id := range mapped {
		if !seen[id] {
			t.Errorf("%s is mapped in coverage.json but absent from docs/threat-model.md", id)
		}
	}
}

// TestEveryThreatHasEvidenceOrExplicitGap enforces the v0.1.5 exit criterion:
// each critical/high threat traces to at least one existing test, and any
// remaining hole is an explicitly recorded gap.
func TestEveryThreatHasEvidenceOrExplicitGap(t *testing.T) {
	for id, entry := range loadCoverage(t) {
		if entry.Severity != "critical" && entry.Severity != "high" {
			t.Errorf("%s: severity %q is not critical|high", id, entry.Severity)
		}
		if len(entry.Tests) == 0 {
			t.Errorf("%s: no test evidence; the ledger only registers threats with regression tests", id)
		}
		for _, gap := range entry.Gaps {
			if strings.TrimSpace(gap) == "" {
				t.Errorf("%s: empty gap entry", id)
			}
		}
	}
}

// TestReferencedTestsExist verifies each mapped test function is still
// defined in the referenced file, keeping the mapping honest across renames.
func TestReferencedTestsExist(t *testing.T) {
	for id, entry := range loadCoverage(t) {
		for _, ref := range entry.Tests {
			source, err := os.ReadFile(filepath.Join(repoRoot, ref.File))
			if err != nil {
				t.Errorf("%s: referenced file %s: %v", id, ref.File, err)
				continue
			}
			if !strings.Contains(string(source), "func "+ref.Name+"(") {
				t.Errorf("%s: test %s not found in %s", id, ref.Name, ref.File)
			}
		}
	}
}
