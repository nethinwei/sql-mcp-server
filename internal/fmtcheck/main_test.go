package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTreeGofmt(t *testing.T) {
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
	if len(got) != 1 || got[0].path != unformatted || got[0].detail != "not gofmt-ed" {
		t.Fatalf("issues = %#v, want unformatted gofmt issue", got)
	}
}

func TestCheckFileLineLength(t *testing.T) {
	longLine := strings.Repeat("x", maxLineColumns+1)
	source := []byte("package sample\n\n// " + longLine + "\n")
	issues, err := checkFile("sample.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].detail, "line 3 has") {
		t.Fatalf("issues = %#v, want line length violation", issues)
	}
}

func TestCheckFileTooManyLines(t *testing.T) {
	lines := make([]string, maxFileLines+1)
	for i := range lines {
		lines[i] = "// line"
	}
	source := []byte("package sample\n\n" + strings.Join(lines, "\n") + "\n")
	issues, err := checkFile("sample.go", source)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range issues {
		if strings.Contains(item.detail, "file has") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %#v, want file length violation", issues)
	}
}

func TestCheckFileFunctionLength(t *testing.T) {
	body := strings.Repeat("\n\t_ = 0", maxFuncLines)
	source := []byte("package sample\n\nfunc longFunc() {" + body + "\n}\n")
	issues, err := checkFile("sample.go", source)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range issues {
		if strings.Contains(item.detail, "function longFunc has") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %#v, want function length violation", issues)
	}
}

func TestFuncName(t *testing.T) {
	source := []byte("package sample\n\nfunc f() {}\nfunc (s *Server) Run() {}\n")
	issues, err := checkFile("sample.go", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range issues {
		if strings.Contains(item.detail, "function") {
			t.Fatalf("unexpected function issue: %#v", item)
		}
	}
}
