package next_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outsideTagMentions lists the non-test Go files that mention <outside> in
// documentation or system-prompt prose rather than building a wrapper.
var outsideTagMentions = map[string]bool{
	"cmd/dyna/subscription.go":   true,
	"contract/tools/methods.go":  true,
	"contract/tools/toolkits.go": true,
	"engine/prompt/prompt.go":    true,
}

// outsideWrapFiles lists every non-test Go file outside contract/outside that
// delivers outside text through outside.Wrap.
var outsideWrapFiles = []string{
	"box/dynaext/dynaext.go",
	"engine/discord_read.go",
	"engine/issue_track.go",
	"engine/navigation.go",
	"engine/post_forward.go",
	"engine/pr_track.go",
	"engine/websearch_api.go",
	"room/delivery_render.go",
}

// TestOutsideWrapperStaysInOneFunction enforces that no code outside
// contract/outside builds its own <outside> wrapper, and that every outside
// delivery path imports contract/outside and calls outside.Wrap.
func TestOutsideWrapperStaysInOneFunction(t *testing.T) {
	found, err := handBuiltOutsideWrappers(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Error(f + ": hand-built <outside> wrapper outside contract/outside")
	}
	for _, rel := range outsideWrapFiles {
		ok, err := callsOutsideWrap(rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if !ok {
			t.Errorf("%s: expected outside.Wrap call", rel)
		}
	}
}

func TestOutsideGuardCatchesHandBuiltWrapper(t *testing.T) {
	root := t.TempDir()
	src := "package p\n\nfunc wrap(s string) string { return \"<outside source=\\\"x\\\">\\n\" + s + \"\\n</outside>\" }\n"
	if err := os.WriteFile(filepath.Join(root, "p.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := handBuiltOutsideWrappers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("got %v, want 2 violations (open and close literals)", found)
	}
}

func handBuiltOutsideWrappers(root string) ([]string, error) {
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
			if rel == "third_party" || rel == "web" || rel == "contract/outside" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || outsideTagMentions[rel] {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok {
				return true
			}
			text, ok := stringValue(lit)
			if !ok {
				return true
			}
			lower := strings.ToLower(text)
			if strings.Contains(lower, "<outside") || strings.Contains(lower, "</outside") {
				found = append(found, fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	return found, err
}

func callsOutsideWrap(path string) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}
	var called bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wrap" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "outside" {
			called = true
		}
		return true
	})
	return called, nil
}
