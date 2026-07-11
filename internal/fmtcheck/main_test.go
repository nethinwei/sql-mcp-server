package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckTree(t *testing.T) {
	root := t.TempDir()
	formatted := filepath.Join(root, "formatted.go")
	unformatted := filepath.Join(root, "unformatted.go")
	if err := os.WriteFile(formatted, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unformatted, []byte("package sample\n\nfunc f( ){ }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := checkTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != unformatted {
		t.Fatalf("unformatted = %v, want [%s]", got, unformatted)
	}
}
