package workflow

import (
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
)

func TestJobResumeParallelBranchesKeepCallIdentity(t *testing.T) {
	script := `
async function branch(shouldYield, command) {
  if (shouldYield) await Promise.resolve()
  return (await job({command: [command]})).id
}
return await parallel([
  () => branch(args.wait === 'a', 'a'),
  () => branch(args.wait === 'b', 'b'),
])
`
	source, err := runstore.Create("job parallel source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil {
		t.Fatalf("first run = %s, %v", raw, err)
	}
	want := raw
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("job parallel resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != want {
		t.Fatalf("reordered resume = %s, %v; want original branch mapping %s", raw, err, want)
	}
	if got := len(fake.JobStarts()); got != 2 {
		t.Fatalf("RunJob calls = %d, want no replay submissions", got)
	}
}

func TestJobResumeRejectsIndistinguishableIdenticalCalls(t *testing.T) {
	script := `
async function branch(shouldYield) {
  if (shouldYield) await Promise.resolve()
  return (await job({command: ['same']})).exitCode
}
return await Promise.all([
  branch(args.wait === 'a'),
  branch(args.wait === 'b'),
])
`
	source, err := runstore.Create("ambiguous job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobExitCodes: map[string]int{"job_1": 0, "job_2": 7}}
	if _, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a"}, Engine: fake, JobNamespace: source.Meta.ID}); err != nil {
		t.Fatal(err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("ambiguous job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err == nil {
		t.Fatal("ambiguous identical replay succeeded")
	}
	if got := len(fake.JobStarts()); got != 2 {
		t.Fatalf("RunJob calls = %d, want no ambiguous replay submissions", got)
	}
}

func TestJobResumeParallelIdenticalCallsKeepResults(t *testing.T) {
	script := `
async function branch(shouldYield) {
  if (shouldYield) await Promise.resolve()
  return (await job({command: ['same']})).exitCode
}
return await parallel([
  () => branch(args.wait === 'a'),
  () => branch(args.wait === 'b'),
])
`
	source, err := runstore.Create("identical job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobExitCodes: map[string]int{"job_1": 0, "job_2": 7}}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil {
		t.Fatalf("first run = %s, %v", raw, err)
	}
	want := raw
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("identical job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != want {
		t.Fatalf("reordered identical resume = %s, %v; want original results %s", raw, err, want)
	}
	if got := len(fake.JobStarts()); got != 2 {
		t.Fatalf("RunJob calls = %d, want no replay submissions", got)
	}
}
