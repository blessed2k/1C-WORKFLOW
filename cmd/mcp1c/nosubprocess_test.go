package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// spawningCalls are the ways this package could start a process without importing
// os/exec, keyed by import path and not by the name written in the source. An
// import list alone would pass a syscall.ForkExec written by hand; matching the
// identifier alone would pass that same call behind `import sys "syscall"`.
var spawningCalls = map[string]map[string]bool{
	"os":      {"StartProcess": true},
	"syscall": {"StartProcess": true, "ForkExec": true, "Exec": true},
}

// TestServerStartsNoSubprocess: the server must not fork anything, on any
// platform. The rule the run is built on is one long-lived process that stops
// spending what it does not have to — and a tool that forks a helper per call is
// exactly that spending, doubly so for server_info, which is called to find out
// where the memory went.
//
// The files are parsed, not compiled, so the platform-specific ones are all read
// here regardless of which platform runs the test: the darwin file cannot smuggle
// a ps back in while the Linux CI stays green. Test files are outside the rule —
// measuring the server from outside is what a test is for.
func TestServerStartsNoSubprocess(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++

		// What a selector's left-hand identifier means is decided by this file's
		// import list, so the local name is only a key into it and never the thing
		// compared: an alias renames the spelling, not the package.
		importedAs := make(map[string]string, len(file.Imports))
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import path %s: %v", name, imp.Path.Value, err)
			}
			if path == "os/exec" {
				t.Errorf("%s imports os/exec: the server runs no external processes", name)
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			// A blank or dot import binds no qualifier to match on. The default
			// above is the last path element, which is exact for the packages
			// watched here: os and syscall are named after theirs.
			if local == "_" || local == "." {
				continue
			}
			importedAs[local] = path
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, imported := importedAs[qualifier.Name]
			if !imported {
				return true
			}
			if spawningCalls[path][sel.Sel.Name] {
				t.Errorf("%s:%d calls %s.%s, written %s.%s: the server runs no external processes",
					name, fset.Position(sel.Pos()).Line,
					path, sel.Sel.Name, qualifier.Name, sel.Sel.Name)
			}
			return true
		})
	}
	// A rule that silently checked nothing would be worse than no rule: the
	// package has dozens of non-test files, and a handful means the walk broke.
	if checked < 10 {
		t.Fatalf("only %d non-test files parsed; the check is not looking at the package", checked)
	}
}
