package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
)

func checkFile(path string, source []byte) ([]issue, error) {
	var issues []issue
	issues = append(issues, checkFileLines(path, source)...)
	issues = append(issues, checkLineLengths(path, source)...)

	gofmtIssues, err := checkGofmt(path, source)
	if err != nil {
		return nil, err
	}
	issues = append(issues, gofmtIssues...)

	funcIssues, err := checkFuncLengths(path, source)
	if err != nil {
		return nil, err
	}
	issues = append(issues, funcIssues...)
	return issues, nil
}

func checkFileLines(path string, source []byte) []issue {
	lines := countLines(source)
	if lines <= maxFileLines {
		return nil
	}
	return []issue{{
		path:   path,
		detail: fmt.Sprintf("file has %d lines (max %d)", lines, maxFileLines),
	}}
}

func checkLineLengths(path string, source []byte) []issue {
	var issues []issue
	for lineNo, line := range splitLines(source) {
		if len(line) > maxLineColumns {
			issues = append(issues, issue{
				path: path,
				detail: fmt.Sprintf(
					"line %d has %d columns (max %d)",
					lineNo+1,
					len(line),
					maxLineColumns,
				),
			})
		}
	}
	return issues
}

func checkGofmt(path string, source []byte) ([]issue, error) {
	formatted, err := format.Source(source)
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", path, err)
	}
	if bytes.Equal(source, formatted) {
		return nil, nil
	}
	return []issue{{path: path, detail: "not gofmt-ed"}}, nil
}

func checkFuncLengths(path string, source []byte) ([]issue, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var issues []issue
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		lines := end - start + 1
		if lines > maxFuncLines {
			issues = append(issues, issue{
				path: path,
				detail: fmt.Sprintf(
					"function %s has %d lines (max %d)",
					funcName(fn),
					lines,
					maxFuncLines,
				),
			})
		}
		return true
	})
	return issues, nil
}

func countLines(source []byte) int {
	if len(source) == 0 {
		return 0
	}
	trimmed := strings.TrimRight(string(source), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func splitLines(source []byte) []string {
	if len(source) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	switch t := recv.(type) {
	case *ast.Ident:
		return fmt.Sprintf("(%s).%s", t.Name, fn.Name.Name)
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return fmt.Sprintf("(*%s).%s", ident.Name, fn.Name.Name)
		}
	}
	return fn.Name.Name
}
