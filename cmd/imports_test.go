package cmd

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/DomBlack/git-stack"

// TestImportRules enforces the layering rules from AGENTS.md by parsing the
// import declarations of every Go file in the module.
func TestImportRules(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	type file struct {
		rel     string
		imports []string
	}
	var files []file
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Like the go tool, skip dot dirs (.git, and agent worktrees under
			// .claude) as well as testdata and build output.
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		var imports []string
		for _, imp := range f.Imports {
			imports = append(imports, strings.Trim(imp.Path.Value, `"`))
		}
		files = append(files, file{rel: rel, imports: imports})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found")
	}

	isAdapter := func(pkg string) bool {
		rel := strings.TrimPrefix(pkg, modulePath+"/")
		return strings.HasPrefix(rel, "pkg/backend/") ||
			(strings.HasPrefix(rel, "pkg/forge/") && rel != "pkg/forge") ||
			(strings.HasPrefix(rel, "pkg/ai/") && rel != "pkg/ai")
	}
	adapterWiring := []string{"cmd/root.go", "cmd/mcp.go"}

	for _, f := range files {
		dir := filepath.ToSlash(filepath.Dir(f.rel))
		isTest := strings.HasSuffix(f.rel, "_test.go")
		for _, imp := range f.imports {
			switch {
			case imp == "os/exec" && dir != "pkg/exec":
				t.Errorf("%s imports os/exec; use pkg/exec", f.rel)
			case strings.HasPrefix(dir, "pkg/") && imp == modulePath+"/cmd":
				t.Errorf("%s: pkg/ must not import cmd/", f.rel)
			case isAdapter(imp) && strings.HasPrefix(dir, "cmd") && !isTest && !slices.Contains(adapterWiring, f.rel):
				t.Errorf("%s imports adapter %s; only %v may", f.rel, imp, adapterWiring)
			case isAdapter(imp) && isAdapter(modulePath+"/"+dir) && !strings.HasPrefix(imp, modulePath+"/"+dir):
				t.Errorf("%s: adapters must not import each other (%s)", f.rel, imp)
			case isAdapter(imp) && (dir == "pkg/stack" || dir == "pkg/forge" || dir == "pkg/ai" || dir == "pkg/app" || dir == "pkg/mcp"):
				t.Errorf("%s: ports and use cases must not import adapters (%s)", f.rel, imp)
			case dir == "pkg/mcp" && imp == modulePath+"/pkg/ui":
				t.Errorf("%s: pkg/mcp must not import pkg/ui", f.rel)
			case strings.HasPrefix(imp, "github.com/charmbracelet/bubbletea") ||
				strings.HasPrefix(imp, "github.com/charmbracelet/lipgloss") ||
				strings.HasPrefix(imp, "github.com/charmbracelet/bubbles"):
				t.Errorf("%s imports Charm v1 package %s; use charm.land/*/v2", f.rel, imp)
			case strings.HasPrefix(imp, "charm.land/") && dir != "pkg/ui" && !strings.HasPrefix(dir, "pkg/ui/"):
				t.Errorf("%s imports %s; TUI code lives in pkg/ui", f.rel, imp)
			}
		}
	}

	// Sanity: the rules are live (pkg/exec really does import os/exec).
	found := false
	for _, f := range files {
		if f.rel == "pkg/exec/runner.go" && slices.Contains(f.imports, "os/exec") {
			found = true
		}
	}
	if !found {
		t.Error("expected pkg/exec/runner.go to import os/exec")
	}
}
