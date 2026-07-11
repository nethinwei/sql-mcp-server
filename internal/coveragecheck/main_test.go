package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	profile := "mode: atomic\nexample/a.go:1.1,2.1 3 1\nexample/a.go:4.1,5.1 1 0\n"
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := profileCoverage(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 75 {
		t.Fatalf("coverage = %v, want 75", got)
	}
}

func TestProfileCoverageRejectsEmptyProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte("mode: atomic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := profileCoverage(path); err == nil {
		t.Fatal("empty profile should fail")
	}
}
