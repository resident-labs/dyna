package callerid

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// launchers are the functions that start managed work. Each must use its
// shared recording path before a caller can reach the engine.
var launchers = map[string]struct{ function, recorder string }{
	"internal/subagent/manager.go":         {"Spawn", "Start"},
	"internal/subagent/launch_identity.go": {"startDetached", "Start"},
	"internal/workflowsvc/launch.go":       {"Launch", "Start"},
	"internal/shells/shells.go":            {"StartSupervisor", "Start"},
	"internal/hooks/launch.go":             {"bindHook", "RecordHook"},
}

// Every launch path records its process through its shared protocol, and
// no other code makes an agent scope, which would be unrecorded and refused.
func TestEveryLaunchPathRecords(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/callerid/") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					switch {
					case pkg.Name == "agentslice" && sel.Sel.Name == "Wrap":
						t.Errorf("%s %s makes an agent scope without a record; use callerid.Start", rel, fn.Name.Name)
					case pkg.Name == "callerid":
						launcher, ok := launchers[rel]
						if ok && sel.Sel.Name == launcher.recorder && fn.Name.Name == launcher.function {
							found[rel] = true
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for file, launch := range launchers {
		if !found[file] {
			t.Errorf("%s %s does not record its process with callerid.%s; if you renamed or moved it, rename it here too", file, launch.function, launch.recorder)
		}
	}
}
