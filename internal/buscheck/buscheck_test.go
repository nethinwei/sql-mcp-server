package buscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Production code starts its goroutines through the execution bus.
func TestGoroutinesGoThroughTheBus(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	found, err := Check(root, "./core/...", "./x/...", "./cmd/...")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("goroutines off the execution bus:\n%s", strings.Join(found, "\n"))
	}
}

// The check finds each way of starting a goroutine.
func TestCheckFindsSpawners(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.25\n")
	write("m.go", `package m

import (
	"sync"
	"time"
)

func F() {
	go func() {}()
	var wg sync.WaitGroup
	wg.Go(func() {})
	time.AfterFunc(time.Second, func() {})
}
`)
	found, err := Check(dir, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("found %d, want 3:\n%s", len(found), strings.Join(found, "\n"))
	}
}
