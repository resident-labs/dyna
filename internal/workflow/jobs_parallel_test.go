package workflow

import (
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
)

func TestJobResumeParallelBranchesKeepCallIdentity(t *testing.T) {
	script := `
return await parallel([
  async () => {
    if (args.wait === 'a') await Promise.resolve()
    return (await job({command: ['a']})).id
  },
  async () => {
    if (args.wait === 'b') await Promise.resolve()
    return (await job({command: ['b']})).id
  },
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

func TestJobResumeNestedParallelDifferentCommandsKeepJobs(t *testing.T) {
	script := `
return await parallel([
  async () => await parallel([
    async () => {
      if (args.wait === 'a') await Promise.resolve()
      return (await job({command: ['a']})).id
    },
    async () => {
      if (args.wait === 'b') await Promise.resolve()
      return (await job({command: ['b']})).id
    },
  ]),
])
`
	source, err := runstore.Create("nested different job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil {
		t.Fatalf("nested different source = %s, %v", raw, err)
	}
	want := raw
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("nested different job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != want {
		t.Fatalf("nested different resume = %s, %v; want original mapping %s", raw, err, want)
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

func TestJobResumeRejectsPartialConcurrentJournal(t *testing.T) {
	script := `
async function branch(shouldYield, skip) {
  if (skip) return -1
  if (shouldYield) await Promise.resolve()
  return (await job({command: ['same']})).exitCode
}
return await Promise.all([
  branch(args.wait === 'a', args.skip === 'a'),
  branch(args.wait === 'b', args.skip === 'b'),
])
`
	source, err := runstore.Create("partial job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobExitCodes: map[string]int{"job_1": 0, "job_2": 7}}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a", "skip": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil || raw != `[-1,0]` {
		t.Fatalf("partial source = %s, %v", raw, err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("partial job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err == nil {
		t.Fatal("partial concurrent journal replay succeeded")
	}
	if got := len(fake.JobStarts()); got != 1 {
		t.Fatalf("RunJob calls = %d, want no partial-journal replay submission", got)
	}
}

func TestJobResumeRejectsNestedPartialConcurrentJournal(t *testing.T) {
	script := `
async function nested(shouldYield, skip) {
  if (skip) return -1
  if (shouldYield) await Promise.resolve()
  return (await job({command: ['same']})).exitCode
}
return await parallel([
  async () => await Promise.all([
    nested(args.wait === 'a', args.skip === 'a'),
    nested(args.wait === 'b', args.skip === 'b'),
  ]),
])
`
	source, err := runstore.Create("nested partial job source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobExitCodes: map[string]int{"job_1": 0, "job_2": 7}}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a", "skip": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil || raw != `[[-1,0]]` {
		t.Fatalf("nested partial source = %s, %v", raw, err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("nested partial job resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != `[null]` {
		t.Fatalf("nested partial resume = %s, %v; want rejected branch calls", raw, err)
	}
	if got := len(fake.JobStarts()); got != 1 {
		t.Fatalf("RunJob calls = %d, want no nested partial-journal submission", got)
	}
}

func TestJobResumeRejectsConcurrentRepeatedGroups(t *testing.T) {
	script := `
async function repeated(shouldYield, skip) {
  return await parallel([
    async () => {
      if (skip) return -1
      if (shouldYield) await Promise.resolve()
      return (await job({command: ['same']})).exitCode
    },
  ])
}
return await Promise.all([
  repeated(args.wait === 'a', args.skip === 'a'),
  repeated(args.wait === 'b', args.skip === 'b'),
])
`
	source, err := runstore.Create("repeated group source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobExitCodes: map[string]int{"job_1": 0, "job_2": 7}}
	raw, _, err := runCommandScript(t, script, Options{Run: source, Args: map[string]any{"wait": "a", "skip": "a"}, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil || raw != `[[-1],[0]]` {
		t.Fatalf("repeated group source = %s, %v", raw, err)
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("repeated group resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Args: map[string]any{"wait": "b"}, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != `[[null],[null]]` {
		t.Fatalf("repeated group resume = %s, %v; want rejected group calls", raw, err)
	}
	if got := len(fake.JobStarts()); got != 1 {
		t.Fatalf("RunJob calls = %d, want no repeated-group replay submission", got)
	}
}

func TestJobResumeParallelIdenticalCallsKeepResults(t *testing.T) {
	script := `
return await parallel([
  async () => {
    if (args.wait === 'a') await Promise.resolve()
    return (await job({command: ['same']})).exitCode
  },
  async () => {
    if (args.wait === 'b') await Promise.resolve()
    return (await job({command: ['same']})).exitCode
  },
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
