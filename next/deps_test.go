package next_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/resident-labs/dyna"

// layoutRule says what one top-level package under next/ may import.
type layoutRule struct {
	// imports lists module packages by path relative to the module. An entry
	// also covers its subpackages.
	imports []string
	// stdlibOnly rejects every import outside the standard library.
	stdlibOnly bool
}

// LEGACY(v1-transfer): the migrate row of layoutRules, for the migrate package. Remove after the transfer.
// layoutRules is the table in spec section 2. cmd/dyna is not listed, so it may import anything.
var layoutRules = map[string]layoutRule{
	"contract": {stdlibOnly: true},
	"room":     {imports: []string{"contract"}},
	"harness":  {imports: []string{"contract"}},
	"box":      {imports: []string{"contract", "harness"}},
	"engine":   {imports: []string{"room", "box", "harness", "contract", "web"}},
	"migrate":  {imports: []string{"contract", "engine/store"}},
}

// violation returns why pkg may not import imp, or "" when it may.
func (r layoutRule) violation(pkg, imp string) string {
	rel, inModule := strings.CutPrefix(imp, modulePath+"/")
	if !inModule {
		first, _, _ := strings.Cut(imp, "/")
		if r.stdlibOnly && strings.Contains(first, ".") {
			return "is not in the standard library, which spec section 2 requires"
		}
		return ""
	}
	if rel == pkg || strings.HasPrefix(rel, pkg+"/") {
		return ""
	}
	for _, allowed := range r.imports {
		if rel == allowed || strings.HasPrefix(rel, allowed+"/") {
			return ""
		}
	}
	return "is not allowed by the dependency table in spec section 2"
}

// importViolations parses the imports of every Go file under each rule's package
// directory in root. It returns one message per forbidden import.
func importViolations(root string, rules map[string]layoutRule) ([]string, error) {
	var violations []string
	fset := token.NewFileSet()
	for pkg, rule := range rules {
		err := filepath.WalkDir(filepath.Join(root, pkg), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if msg := rule.violation(pkg, imp); msg != "" {
					violations = append(violations, path+" imports "+imp+" and "+msg)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

func TestDependencyDirection(t *testing.T) {
	violations, err := importViolations(".", layoutRules)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// plantHarnessFile writes harness/harness.go with the given import and returns its violations.
func plantHarnessFile(t *testing.T, imp string) []string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "harness")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package harness\n\nimport _ \"" + imp + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "harness.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err := importViolations(root, map[string]layoutRule{"harness": layoutRules["harness"]})
	if err != nil {
		t.Fatal(err)
	}
	return violations
}

func TestDependencyDirectionReportsForbiddenImport(t *testing.T) {
	if violations := plantHarnessFile(t, modulePath+"/box"); len(violations) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(violations), violations)
	}
}

func TestDependencyDirectionAllowsOwnArea(t *testing.T) {
	if violations := plantHarnessFile(t, modulePath+"/harness/fake"); len(violations) != 0 {
		t.Fatalf("got violations for own subpackage: %v", violations)
	}
}
