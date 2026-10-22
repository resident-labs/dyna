package authority

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fixed authority.Actor{Role: authority.Owner} in code that an engine call
// reaches gives every caller the owner's power (requirements item 3). Code
// takes its actor from authority.Of(ctx) instead. internal/engine states the
// owner for its own background work, so it is not counted. The listed uses
// may only shrink.
func TestOwnerLiteralsOnlyShrink(t *testing.T) {
	allowed := map[string]int{}
	list, err := os.Open("testdata/owner-literals.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Close()
	lines := bufio.NewScanner(list)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var file string
		var n int
		if _, err := fmt.Sscan(line, &file, &n); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		allowed[file] = n
	}
	root := filepath.Join("..", "..")
	found := map[string]int{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/authority/") || strings.HasPrefix(rel, "internal/engine/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// Parse only files that can hold the literal, so the scan stays fast.
			if !bytes.Contains(src, []byte("authority.Owner")) {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && isAuthority(lit.Type, "Actor") {
					for _, elt := range lit.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "Role") && isAuthority(kv.Value, "Owner") {
							found[rel]++
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for file, n := range found {
		if n > allowed[file] {
			t.Errorf("%s has %d fixed owner actors, %d allowed: take the actor from authority.Of(ctx)", file, n, allowed[file])
		}
	}
	for file, n := range allowed {
		if found[file] < n {
			t.Errorf("%s now has %d fixed owner actors: lower its line in testdata/owner-literals.txt to %d", file, found[file], found[file])
		}
	}
}

func isAuthority(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "authority"
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
