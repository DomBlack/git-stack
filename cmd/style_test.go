package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestNoRawPrintingInCommands enforces docs/style.md: every human facing
// line goes through ui.Reporter so all commands look the same. The only
// exceptions print machine output (the completion script, version strings).
func TestNoRawPrintingInCommands(t *testing.T) {
	allowed := []string{"completion.go", "version.go"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || slices.Contains(allowed, filepath.Base(path)) {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "fmt" {
				return true
			}
			if strings.HasPrefix(sel.Sel.Name, "Print") || strings.HasPrefix(sel.Sel.Name, "Fprint") {
				t.Errorf("%s: %s uses fmt.%s; print through ui.Reporter instead (docs/style.md)", path, fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
