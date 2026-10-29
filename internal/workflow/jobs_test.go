package workflow

import (
	"encoding/json"
	"slices"
	"testing"

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
	script := `return await job({command: ['make', 'test'], node: 'builder'})`
	source, err := runstore.Create("job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{}
	if _, _, err := runCommandScript(t, script, Options{Run: source, Engine: fake, JobNamespace: source.Meta.ID}); err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "job" {
			entries[i].State = "error"
			entries[i].Error = "context canceled"
			break
		}
	}
	resumed, err := runstore.Create("job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := runCommandScript(t, script, Options{Run: resumed, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result jobResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "job_1" || result.State != "passed" {
		t.Fatalf("resumed result = %+v", result)
	}
	if starts := fake.JobStarts(); len(starts) != 1 {
		t.Fatalf("total submissions = %d, want only the source job", len(starts))
	}
	waits := fake.JobWaits()
	if len(waits) != 2 || waits[1] != "job_1" {
		t.Fatalf("WaitJob IDs = %v, want resume to reattach to job_1", waits)
	}
}
