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

// sqlCall is one SQL call found in a file.
type sqlCall struct {
	file string
	line int
	name string
}

// TestSQLCallsStayInStore keeps raw SQL and database/sql imports inside engine/store.
func TestSQLCallsStayInStore(t *testing.T) {
	calls, err := sqlCallsOutsideStore(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		t.Errorf("%s:%d: %s outside engine/store", c.file, c.line, c.name)
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
