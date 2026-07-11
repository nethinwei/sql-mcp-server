package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("profile", "coverage.txt", "Go coverage profile")
	minimum := flag.Float64("min", 0, "minimum statement coverage percentage")
	flag.Parse()

	coverage, err := profileCoverage(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("core coverage: %.1f%% (minimum %.1f%%)\n", coverage, *minimum)
	if coverage < *minimum {
		fmt.Fprintf(os.Stderr, "core coverage %.1f%% below %.1f%%\n", coverage, *minimum)
		os.Exit(1)
	}
}

func profileCoverage(path string) (float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open coverage profile: %w", err)
	}
	defer file.Close()

	var total, covered int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "mode: ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return 0, fmt.Errorf("invalid coverage profile line %q", line)
		}
		statements, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse statement count in %q: %w", line, err)
		}
		count, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse execution count in %q: %w", line, err)
		}
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read coverage profile: %w", err)
	}
	if total == 0 {
		return 0, fmt.Errorf("coverage profile contains no statements")
	}
	return float64(covered) * 100 / float64(total), nil
}
