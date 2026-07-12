// Command workloadgen renders the checked-in fixtures/v4 artifacts from the
// deterministic generator: per-module DDL and seed SQL for every dialect,
// expected task results (CSV), per-module entity policies, and the runnable
// combined profile. Regenerating with an unchanged generator is a no-op;
// a drift test asserts the checked-in files match.
//
// Usage: go run ./fixtures/v4/generator/cmd/workloadgen [-root fixtures/v4]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	workload "github.com/nethinwei/sql-mcp-server/fixtures/v4/generator"
)

func main() {
	root := flag.String("root", "fixtures/v4", "output root directory")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string) error {
	for path, content := range workload.Artifacts(workload.DefaultConfig()) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
