package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nethinwei/sql-mcp-server/internal/evalcoverage"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	meta := filepath.Join(root, "fixtures/v4/tasks-v5/guided-metadata.yaml")
	add := filepath.Join(root, "fixtures/v4/tasks-v5/additions.yaml")
	dimensions := filepath.Join(root, "fixtures/v4/coverage/dimensions.yaml")
	tasks, err := evalcoverage.LoadTasks(meta, add, dimensions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	matrix := evalcoverage.BuildMatrix(tasks)
	outDir := filepath.Join(root, "fixtures/v4/coverage")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jsonPath := filepath.Join(outDir, "matrix.json")
	data, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mdPath := filepath.Join(outDir, "matrix.md")
	if err := os.WriteFile(mdPath, []byte(evalcoverage.Markdown(matrix)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s and %s (%d tasks)\n", jsonPath, mdPath, len(tasks))
}
