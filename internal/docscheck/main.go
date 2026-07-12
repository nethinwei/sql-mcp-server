// Command docscheck enforces documentation consistency (roadmap v0.1.9
// accompanying item): every relative markdown link resolves to an existing
// file, and the pinned version references (README GA line, roadmap baseline,
// release notes, CHANGELOG) agree with each other.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skipDirs are never scanned (generated, external, or VCS content).
var skipDirs = map[string]bool{".git": true, "dist": true, "node_modules": true}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	var problems []string
	problems = append(problems, checkLinks(root)...)
	problems = append(problems, checkVersions(root)...)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "\ndocscheck: %d problem(s)\n", len(problems))
		os.Exit(1)
	}
	fmt.Println("docscheck: ok")
}

// markdownLink matches inline links and images; reference-style links are
// not used in this repository.
var markdownLink = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)\)`)

func checkLinks(root string) []string {
	var problems []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := match[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			if target == "" {
				continue
			}
			resolved := filepath.Join(filepath.Dir(path), target)
			if _, err := os.Stat(resolved); err != nil {
				problems = append(problems,
					fmt.Sprintf("%s: broken link %q (%s)", path, match[1], resolved))
			}
		}
		return nil
	})
	if err != nil {
		problems = append(problems, fmt.Sprintf("walk: %v", err))
	}
	return problems
}

// version reference extractors. Both lines are stable contract text: the
// README "当前 GA" line and the roadmap "当前稳定基线" line.
var (
	readmeGA        = regexp.MustCompile("当前 GA：`(v[0-9]+\\.[0-9]+\\.[0-9]+)`")
	roadmapBaseline = regexp.MustCompile("当前稳定基线为 `(v[0-9]+\\.[0-9]+\\.[0-9]+)`")
)

func checkVersions(root string) []string {
	var problems []string
	readme := extract(filepath.Join(root, "README.md"), readmeGA, &problems)
	roadmap := extract(filepath.Join(root, "docs", "roadmap.md"), roadmapBaseline, &problems)
	if readme == "" || roadmap == "" {
		return problems
	}
	if readme != roadmap {
		problems = append(problems, fmt.Sprintf(
			"version drift: README GA is %s but docs/roadmap.md baseline is %s", readme, roadmap))
	}
	releaseNote := filepath.Join(root, "docs", "releases", readme+".md")
	if _, err := os.Stat(releaseNote); err != nil {
		problems = append(problems, fmt.Sprintf(
			"version drift: README GA is %s but %s does not exist", readme, releaseNote))
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		problems = append(problems, fmt.Sprintf("CHANGELOG.md: %v", err))
	} else if !strings.Contains(string(changelog), readme) {
		problems = append(problems, fmt.Sprintf(
			"version drift: README GA is %s but CHANGELOG.md does not mention it", readme))
	}
	return problems
}

func extract(path string, pattern *regexp.Regexp, problems *[]string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("%s: %v", path, err))
		return ""
	}
	match := pattern.FindSubmatch(data)
	if match == nil {
		*problems = append(*problems, fmt.Sprintf(
			"%s: pinned version line not found (pattern %s)", path, pattern))
		return ""
	}
	return string(match[1])
}
