package next_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// dataSegments are the names of Dyna's data folders. box/layout.go alone joins them.
var dataSegments = []string{"rooms", "files", "transcripts", "instructions", "shared", "git", "cache", "bin", "sock", "store.db", "backups", "archive", "connect", "cliproxy", "AGENTS.md", "compose"}

// layoutOwners holds the files that may name these segments: Layout itself, and the migration bundle, which is a separate folder with its own rooms/.
var layoutOwners = map[string]bool{"box/layout.go": true, "migrate/dryrun.go": true}

// shellSegment finds a segment joined to a shell variable, as in "$1"/rooms/*/shared.
var shellSegment = regexp.MustCompile(`\$[\w{}]*"?/(` + strings.ReplaceAll(regexp.QuoteMeta(strings.Join(dataSegments, "|")), `\|`, `|`) + `)([/"\s]|$)`)

// TestDataPathsStayInLayout keeps every path of Dyna's data in box.Layout.
func TestDataPathsStayInLayout(t *testing.T) {
	found, err := handBuiltPaths(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Error(f + ": a data path outside box.Layout")
	}
}

// TestDataPathGuardFindsBothForms plants one of each form the guard looks for.
func TestDataPathGuardFindsBothForms(t *testing.T) {
	root := t.TempDir()
	src := "package p\n\nimport \"path/filepath\"\n\nvar a = filepath.Join(\"/d\", \"cliproxy/auth\")\nvar b = `for dir in \"$1\"/rooms/*; do :; done`\nvar c = \"$HOME/.local/opt\"\n"
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := handBuiltPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %v", found)
	}
}

// handBuiltPaths parses the non-test Go files under root, except third_party and the web app, and returns where a filepath.Join or a shell script names a data segment.
func handBuiltPaths(root string) ([]string, error) {
	segments := map[string]bool{}
	for _, s := range dataSegments {
		segments[s] = true
	}
	var found []string
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
			if rel == "third_party" || rel == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || layoutOwners[rel] {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				call, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || call.Sel.Name != "Join" {
					return true
				}
				pkg, ok := call.X.(*ast.Ident)
				if !ok || (pkg.Name != "filepath" && pkg.Name != "path") {
					return true
				}
				for _, arg := range n.Args {
					text, ok := stringValue(arg)
					if !ok {
						continue
					}
					for _, part := range strings.Split(text, "/") {
						if segments[part] {
							found = append(found, fset.Position(arg.Pos()).String())
						}
					}
				}
			case *ast.BasicLit:
				if text, ok := stringValue(n); ok && shellSegment.MatchString(text) {
					found = append(found, fset.Position(n.Pos()).String())
				}
			}
			return true
		})
		return nil
	})
	return found, err
}

func stringValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	return text, err == nil
}
