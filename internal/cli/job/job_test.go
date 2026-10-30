package job

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callspec"

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

func captureRun(t *testing.T, args ...string) callspec.RunJobInput {
	t.Helper()
	previous := callRunJob
	var captured callspec.RunJobInput
	callRunJob = func(_ context.Context, request callspec.RunJobInput, _ *jobs.Job) error {
		captured = request
		return nil
	}
	t.Cleanup(func() { callRunJob = previous })
	cmd := runCmd()
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return captured
}

func TestRunQuickFlag(t *testing.T) {
	request := captureRun(t, "--quick", "--", "true")
	if request.Lane != string(jobs.LaneQuick) {
		t.Fatalf("lane = %q", request.Lane)
	}
}

func TestRunWithoutImageRunsOnHost(t *testing.T) {
	request := captureRun(t, "--", "true")
	if request.Image != "" {
		t.Fatalf("image = %q, want none: a job without --image runs on the host", request.Image)
	}
}

func TestRunNetworkNoneFlag(t *testing.T) {
	request := captureRun(t, "--image", "alpine", "--network", "none", "--", "true")
	if request.Network != "none" {
		t.Fatalf("network = %q, want none", request.Network)
	}
}

func TestRunMountFlag(t *testing.T) {
	request := captureRun(t, "--image", "alpine", "--mount", "data:/data:ro", "--mount", "cache:/cache", "--", "true")
	if strings.Join(request.Mounts, ",") != "data:/data:ro,cache:/cache" {
		t.Fatalf("mounts = %v", request.Mounts)
	}
}

func TestRunDeviceFlag(t *testing.T) {
	request := captureRun(t, "--image", "alpine", "--device", "/dev/dri", "--", "true")
	if strings.Join(request.Devices, ",") != "/dev/dri" {
		t.Fatalf("devices = %v", request.Devices)
	}
}
