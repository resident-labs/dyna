package workflow

import (
	"os"
	"path/filepath"
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
