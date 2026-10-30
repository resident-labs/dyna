package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/plan"
)

func TestBundledScriptsRejectMissingArgsAndPlan(t *testing.T) {
	for _, name := range []string{"deep-research", "audit", "fix-until", "migrate"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "workflows", "bundled", name+".js"))
			if err != nil {
				t.Fatal(err)
			}
			p := plan.Extract(string(src), plan.Options{})
			if p.Status == plan.StatusUnavailable {
				t.Fatalf("plan unavailable: %+v", p.Diagnostics)
			}
			_, err = executeWorkflowTest(t, string(src), Options{Store: mockWorkflowStore(0, 0), WorkDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatalf("missing arguments: %v", err)
			}
		})
	}
}

func TestBundledArgumentDeclarations(t *testing.T) {
	cases := []struct {
		name string
		args []plan.WorkflowArg
	}{
		{"deep-research", []plan.WorkflowArg{{Name: "question", Required: true}}},
		{"audit", []plan.WorkflowArg{{Name: "what", Required: true}, {Name: "paths", Required: true}}},
		{"fix-until", []plan.WorkflowArg{{Name: "command", Required: true}, {Name: "maxRounds"}}},
		{"migrate", []plan.WorkflowArg{{Name: "instruction", Required: true}, {Name: "glob", Required: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "workflows", "bundled", tc.name+".js"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := plan.ReadMetadata(string(src))
			if err != nil {
				t.Fatal(err)
			}
			for i := range m.Args {
				if m.Args[i].Description == "" {
					t.Fatal("missing description")
				}
				m.Args[i].Description = ""
			}
			if !reflect.DeepEqual(m.Args, tc.args) {
				t.Fatalf("args = %#v, want %#v", m.Args, tc.args)
			}
		})
	}
}

func TestBundledSingleInputAcceptsString(t *testing.T) {
	for _, name := range []string{"deep-research", "fix-until"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "workflows", "bundled", name+".js"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = executeWorkflowTest(t, string(src), Options{Args: "example", Store: mockWorkflowStore(0, 0), WorkDir: t.TempDir()})
			if err == nil || (!strings.Contains(err.Error(), "requires") && !strings.Contains(err.Error(), "cannot run commands")) {
				t.Fatalf("expected profile validation after accepting string, got %v", err)
			}
		})
	}
}
