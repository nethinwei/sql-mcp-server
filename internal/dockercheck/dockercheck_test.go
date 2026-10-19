// Package dockercheck keeps the image's console build complete: the console
// imports files from outside web/admin (the GraphQL schema, shared fixtures),
// and the Dockerfile's web stage copies only what it names.
package dockercheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// outside matches an import or schema path that leaves web/admin.
var outside = regexp.MustCompile(`['"]((?:\.\./)+(?:core|x)/[^'"]+)['"]`)

// TestDockerfileCopiesWhatTheConsoleReads: every file of the repository the
// console's sources and codegen read from outside web/admin is copied into
// the Dockerfile's web stage.
func TestDockerfileCopiesWhatTheConsoleReads(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	console := filepath.Join(root, "web/admin")
	var files []string
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && (d.Name() == "node_modules" || d.Name() == "gql") {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".vue") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range outside.FindAllStringSubmatch(string(src), -1) {
			rel, err := filepath.Rel(root, filepath.Join(filepath.Dir(path), m[1]))
			if err == nil && !strings.HasPrefix(rel, "..") {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	}
	if err := filepath.WalkDir(filepath.Join(console, "src"), walk); err != nil {
		t.Fatal(err)
	}
	if err := walk(filepath.Join(console, "codegen.ts"), fileEntry{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("found no file the console reads from outside web/admin; the pattern is stale")
	}
	for _, f := range files {
		if !strings.Contains(string(dockerfile), f) {
			t.Errorf("the console reads %s, which the Dockerfile's web stage does not copy", f)
		}
	}
}

// fileEntry stands for a file named directly, not found by the walk.
type fileEntry struct{ fs.DirEntry }

func (fileEntry) IsDir() bool  { return false }
func (fileEntry) Name() string { return "" }
