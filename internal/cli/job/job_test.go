package job

import (
	"testing"
	"time"

	"dyna-agent/internal/jobs"
)

func TestViewShowsIdleTimeout(t *testing.T) {
	if got := view(jobs.Job{Idle: 20 * time.Minute}).Idle; got != "20m0s" {
		t.Fatalf("idle = %q, want 20m0s", got)
	}
}
