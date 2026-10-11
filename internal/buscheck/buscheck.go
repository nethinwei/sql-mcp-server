// Package buscheck keeps the process's goroutines on the execution bus
// (core/bus): its test fails on a go statement, a sync.WaitGroup or errgroup
// Go call, or time.AfterFunc in the production code of core/, x/ and cmd/,
// outside the bus itself and the engine's singleflight, which the Serve lane
// bounds. Conformance suites (packages named *test) are test code.
package buscheck

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// allowed are the files that may start goroutines themselves.
var allowed = []string{"core/bus/bus.go", "core/engine/engine.go"}

// Check loads the packages matching patterns under dir and reports where
// they start a goroutine off the bus.
func Check(dir string, patterns ...string) ([]string, error) {
	cfg := &packages.Config{
		Dir:  dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("%s: %v", pkg.PkgPath, pkg.Errors[0])
		}
		if strings.HasSuffix(pkg.Name, "test") {
			continue
		}
		for _, file := range pkg.Syntax {
			if isAllowed(dir, pkg.Fset.Position(file.Pos()).Filename) {
				continue
			}
			found = append(found, inspect(pkg, file)...)
		}
	}
	return found, nil
}

func isAllowed(dir, name string) bool {
	rel, err := filepath.Rel(dir, name)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		if filepath.ToSlash(rel) == a {
			return true
		}
	}
	return false
}

func inspect(pkg *packages.Package, file *ast.File) []string {
	var found []string
	report := func(n ast.Node, what string) {
		found = append(found, fmt.Sprintf("%s: %s; start it through core/bus", pkg.Fset.Position(n.Pos()), what))
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			report(n, "go statement")
		case *ast.CallExpr:
			if what := spawner(pkg.TypesInfo, n); what != "" {
				report(n, what)
			}
		}
		return true
	})
	return found
}

// spawner names the call when it starts a goroutine.
func spawner(info *types.Info, call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return ""
	}
	name := fn.Pkg().Path() + "." + fn.Name()
	if recv := fn.Signature().Recv(); recv != nil {
		name = types.TypeString(recv.Type(), nil) + "." + fn.Name()
	}
	switch name {
	case "time.AfterFunc", "*sync.WaitGroup.Go", "*golang.org/x/sync/errgroup.Group.Go":
		return name
	}
	return ""
}
