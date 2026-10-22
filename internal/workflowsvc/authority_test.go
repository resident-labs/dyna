package workflowsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/mode"
)

func TestWorkerExecuteRefusesBeforeBackend(t *testing.T) {
	t.Setenv(mode.EnvMode, mode.Workflow)
	_, err := Execute(context.Background(), Options{}, nil, Observer{})
	want := "run is owner or root or session only; use subagent mode to start work"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v; want %q", err, want)
	}
}
