package workflow

import (
	"encoding/json"
	"slices"
	"testing"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
)

func TestJobSubmitsTypedSpecAndReturnsFinalState(t *testing.T) {
	fake := &enginefake.Engine{}
	raw, _, err := runCommandScript(t, `
return await job({
  command: ['go', 'test', './...'],
  labels: ['os=linux', 'gpu'],
  commit: 'HEAD',
  cpus: 6,
  memory: '8g',
  workspace: true,
  timeout: '1h',
  idle: '5m',
  image: 'go:1.25',
  artifacts: ['out/report'],
})
`, Options{Engine: fake})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result jobResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "job_1" || result.Node != "local" || result.State != "passed" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("job result = %+v", result)
	}
	if result.Log != "dyna job logs job_1" {
		t.Fatalf("log = %q, want the dyna job show pointer", result.Log)
	}
	starts := fake.JobStarts()
	if len(starts) != 1 {
		t.Fatalf("RunJob calls = %d, want 1", len(starts))
	}
	spec := starts[0]
	if !slices.Equal(spec.Command, []string{"go", "test", "./..."}) || !slices.Equal(spec.Labels, []string{"os=linux", "gpu"}) {
		t.Fatalf("submitted command or labels = %+v", spec)
	}
	if spec.Repo == "" || spec.Ref != "HEAD" || !spec.Workspace || spec.CPUs != 6 || spec.Memory != "8g" {
		t.Fatalf("submitted source and resources = %+v", spec)
	}
	if spec.Timeout != 3600 || spec.Idle == nil || *spec.Idle != 300 || spec.Image != "go:1.25" || spec.Host {
		t.Fatalf("submitted execution fields = %+v", spec)
	}
	if !slices.Equal(spec.Artifacts, []string{"out/report"}) {
		t.Fatalf("submitted artifacts = %v", spec.Artifacts)
	}
	if spec.IdempotencyKey == "" {
		t.Fatal("workflow submitted no durable idempotency key")
	}
}

func TestJobResumeReusesSubmittedJobAfterInterruptedWait(t *testing.T) {
	key := jobKey("wf_root", "", 1)
	cache := NewCache([]runstore.JournalEntry{
		{ID: 1, Key: key, Kind: "job", State: "running", Job: "job_old"},
		{ID: 1, Key: key, Kind: "job", State: "error", Job: "job_old", Error: "context canceled"},
	})
	code := 0
	fake := &enginefake.Engine{}
	fake.SaveJob(jobs.Job{ID: "job_old", Node: "builder", State: jobs.StatePassed, ExitCode: &code})
	raw, _, err := runCommandScript(t, `return await job({command: ['make', 'test'], node: 'builder'})`, Options{Engine: fake, Cache: cache, JobNamespace: "wf_root"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result jobResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "job_old" || result.State != "passed" {
		t.Fatalf("resumed result = %+v", result)
	}
	if starts := fake.JobStarts(); len(starts) != 0 {
		t.Fatalf("resume submitted %d jobs, want 0: %+v", len(starts), starts)
	}
	if waits := fake.JobWaits(); !slices.Equal(waits, []string{"job_old"}) {
		t.Fatalf("WaitJob IDs = %v, want only job_old", waits)
	}
}
