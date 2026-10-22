package authority

import (
	"bufio"
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

// The engine serves calls for many callers, so code that a call reaches must
// check the caller's actor (Of), not the engine process's own (Current).
// The listed uses may only shrink.
func TestCurrentOnlyShrinks(t *testing.T) {
	allowed := map[string]int{}
	list, err := os.Open("testdata/current-callers.txt")
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
			if strings.HasPrefix(rel, "internal/authority/") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Current" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "authority" {
						found[rel]++
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
			t.Errorf("%s reads authority.Current %d times, %d allowed: check the caller with authority.Of(ctx)", file, n, allowed[file])
		}
	}
	for file, n := range allowed {
		if found[file] < n {
			t.Errorf("%s now reads authority.Current %d times: lower its line in testdata/current-callers.txt to %d", file, found[file], found[file])
		}
	}
}
