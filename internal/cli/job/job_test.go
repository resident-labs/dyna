package job

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
)

func TestViewPrintsResources(t *testing.T) {
	var out strings.Builder
	if err := printJSON(&out, view(jobs.Job{Resources: []machines.ResourceName{"display", "ghidra"}})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"resources":["display","ghidra"]`) {
		t.Fatalf("job JSON lacks its requested resources: %s", out.String())
	}
}

func TestViewShowsIdleTimeout(t *testing.T) {
	if got := view(jobs.Job{Idle: 20 * time.Minute}).Idle; got != "20m0s" {
		t.Fatalf("idle = %q, want 20m0s", got)
	}
}
