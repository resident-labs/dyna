package next_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sqlCallNames are the database/sql methods that code outside engine/store
// must not call. The store owns every query and transaction.
var sqlCallNames = map[string]bool{
	"QueryRowContext": true,
	"QueryContext":    true,
	"ExecContext":     true,
	"PrepareContext":  true,
	"BeginTx":         true,
	"Query":           true,
	"QueryRow":        true,
	"Exec":            true,
}

// sqlAllowlist holds today's SQL calls per file, outside engine/store. Each
// entry counts calls, not lines, so edits nearby do not break it. A count
// lowers as M3 moves calls into the store; a count never rises.
var sqlAllowlist = map[string]int{
	"engine/post_api.go": 4,
	"engine/turn_api.go": 4,
}

// sqlCall is one SQL call found in a file.
type sqlCall struct {
	file string
	line int
	name string
}

// TestSQLCallsStayInStore keeps raw SQL calls inside next/engine/store. A new
// SQL call outside the store fails the test, and so does a file whose count
// drops without its allowlist entry going down.
func TestSQLCallsStayInStore(t *testing.T) {
	calls, err := sqlCallsOutsideStore(".")
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string][]sqlCall{}
	for _, c := range calls {
		byFile[c.file] = append(byFile[c.file], c)
	}
	for file, found := range byFile {
		allowed := sqlAllowlist[file]
		if len(found) > allowed {
			for _, c := range found {
				t.Errorf("%s:%d: %s call; the allowlist allows %d in this file. Add a store method in next/engine/store instead.", c.file, c.line, c.name, allowed)
			}
		}
	}
	for file, allowed := range sqlAllowlist {
		if n := len(byFile[file]); n < allowed {
			t.Errorf("%s has %d SQL calls; lower its sqlAllowlist entry from %d to %d.", file, n, allowed, n)
		}
	}
}

// sqlCallsOutsideStore parses the non-test Go files under root, skipping
// engine/store and third_party, and returns every SQL call and database/sql import.
func sqlCallsOutsideStore(root string) ([]sqlCall, error) {
	var calls []sqlCall
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "engine/store" || d.Name() == "third_party" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		calls = append(calls, sqlCallsIn(fset, rel, f)...)
		return nil
	})
	return calls, err
}

// sqlCallsIn returns the SQL calls in one parsed file. A store write closure
// is a Write call whose second argument is a function literal, because that
// closure receives the *sql.Tx.
func sqlCallsIn(fset *token.FileSet, rel string, f *ast.File) []sqlCall {
	var calls []sqlCall
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == "database/sql" {
			calls = append(calls, sqlCall{file: rel, line: fset.Position(imp.Pos()).Line, name: "database/sql import"})
		}
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
		name := sel.Sel.Name
		// Every database/sql method takes a query or context, so a call with no
		// arguments, such as url.Values from URL.Query(), is not one.
		hasArgs := len(call.Args) > 0
		isWrite := name == "Write" && len(call.Args) == 2 && isFuncLit(call.Args[1])
		if hasArgs && sqlCallNames[name] || isWrite {
			calls = append(calls, sqlCall{file: rel, line: fset.Position(call.Pos()).Line, name: name})
		}
		return true
	})
	return calls
}

func isFuncLit(e ast.Expr) bool {
	_, ok := e.(*ast.FuncLit)
	return ok
}
