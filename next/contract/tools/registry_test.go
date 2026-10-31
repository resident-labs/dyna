package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The table has exactly one row per Method constant, and no row without one.
// The test reads the constants from the contract package itself, so a new
// constant fails until its row exists.
func TestMethodTableCoversEveryConstant(t *testing.T) {
	files, err := filepath.Glob("../*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var constants []contract.Method
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				v, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				id, ok := v.Type.(*ast.Ident)
				if !ok || id.Name != "Method" {
					continue
				}
				for i := range v.Names {
					if i >= len(v.Values) {
						continue
					}
					lit, ok := v.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					constants = append(constants, contract.Method(value))
				}
			}
		}
	}
	sort.Slice(constants, func(i, j int) bool { return constants[i] < constants[j] })
	for i := 1; i < len(constants); i++ {
		if constants[i] == constants[i-1] {
			t.Fatalf("duplicate Method constant %s", constants[i])
		}
	}
	if len(constants) == 0 {
		t.Fatal("no Method constants found")
	}
	rows := Methods()
	count := map[contract.Method]int{}
	for _, row := range rows {
		count[row.Method]++
	}
	for _, m := range constants {
		if count[m] != 1 {
			t.Errorf("method %s has %d rows, want 1", m, count[m])
		}
		delete(count, m)
	}
	for extra := range count {
		t.Errorf("row %s is not a Method constant", extra)
	}
}

func TestRegistryValidation(t *testing.T) {
	if err := Validate(Methods(), BoxActions()); err != nil {
		t.Fatal(err)
	}
	duplicate := append(Methods(), Methods()[0])
	if err := Validate(duplicate, BoxActions()); err == nil {
		t.Fatal("duplicate method row accepted")
	}
	zero := Methods()
	zero[0].Access = 0
	if err := Validate(zero, BoxActions()); err == nil {
		t.Fatal("zero access accepted")
	}
	noCLI := Methods()
	for i := range noCLI {
		if noCLI[i].Method == contract.MethodPost {
			noCLI[i].Actions[0].CLI = nil
		}
	}
	if err := Validate(noCLI, BoxActions()); err == nil {
		t.Fatal("action without CLI accepted")
	}
	conflict := Methods()
	for i := range conflict {
		if conflict[i].Method == contract.MethodPost {
			conflict[i].Actions[1].Request = reflect.TypeFor[struct {
				Text bool `json:"text"`
			}]()
		}
	}
	if err := Validate(conflict, BoxActions()); err == nil {
		t.Fatal("conflicting field accepted")
	}
	box := BoxActions()
	box[0].Handler = NoBoxHandler
	if err := Validate(Methods(), box); err == nil {
		t.Fatal("box action without handler accepted")
	}
}

func TestAgentActionsHideOwnerMethods(t *testing.T) {
	hidden := map[string]bool{}
	for _, row := range Methods() {
		if row.Access == OwnerAccess || row.Access == OwnerLocalAccess {
			for _, a := range row.Actions {
				hidden[a.Tool+"/"+a.Name] = true
			}
		}
	}
	for _, a := range AgentActions() {
		if hidden[a.Tool+"/"+a.Name] {
			t.Fatalf("owner action %s/%s exposed", a.Tool, a.Name)
		}
	}
}

func TestPhaseOneActionCoverage(t *testing.T) {
	// session/restart and room/repos stay owner-only until the authorization PR opens them.
	required := map[string]string{
		"post": "send update_visual", "session": "start list stop close set_profile move", "room": "log pause", "op": "withdraw", "hostexec": "run request_grant", "pr": "open", "visual": "check", "computer": "open click type text screenshot", "context": "show",
	}
	found := map[string]bool{}
	for _, a := range AgentActions() {
		found[a.Tool+"/"+a.Name] = true
	}
	for _, b := range BoxActions() {
		found[b.Tool+"/"+b.Name] = true
	}
	for tool, names := range required {
		for _, name := range strings.Fields(names) {
			if !found[tool+"/"+name] {
				t.Errorf("missing %s/%s", tool, name)
			}
		}
	}
}
