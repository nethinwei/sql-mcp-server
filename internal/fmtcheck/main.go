package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	maxFileLines   = 800
	maxFuncLines   = 50
	maxLineColumns = 120
	golinesModule  = "github.com/segmentio/golines@v0.13.0"
)

type issue struct {
	path   string
	detail string
}

func main() {
	write := flag.Bool("w", false, "format Go sources in place")
	flag.Parse()

	root := "."
	if *write {
		if err := formatTree(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	issues, err := checkTree(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(issues) == 0 {
		return
	}
	for _, item := range issues {
		fmt.Fprintf(os.Stderr, "%s: %s\n", item.path, item.detail)
	}
	os.Exit(1)
}

func formatTree(root string) error {
	cmd := exec.Command(
		"go", "run", golinesModule,
		"-w",
		"-m", strconv.Itoa(maxLineColumns),
		"--base-formatter", "gofmt",
		root,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkTree(root string) ([]issue, error) {
	var issues []issue
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fileIssues, err := checkFile(path, source)
		if err != nil {
			return err
		}
		issues = append(issues, fileIssues...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].path != issues[j].path {
			return issues[i].path < issues[j].path
		}
		return issues[i].detail < issues[j].detail
	})
	return issues, nil
}
