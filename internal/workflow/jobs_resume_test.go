package workflow

import (
	"encoding/json"
	"errors"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow/enginefake"
)

func TestJobResumeDistinguishesIdenticalInvocations(t *testing.T) {
	script := `
const spec = {command: ['make', 'test']}
const first = await job(spec)
const second = await job(spec)
return [first.id, second.id]
`
	source, err := runstore.Create("job duplicate source", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{}
	raw, events, err := runCommandScript(t, script, Options{Run: source, Engine: fake, JobNamespace: source.Meta.ID})
	if err != nil || raw != `["job_1","job_2"]` {
		t.Fatalf("first run = %s, %v; events = %+v; submissions = %+v", raw, err, events, fake.JobStarts())
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runstore.Create("job duplicate resumed", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err = runCommandScript(t, script, Options{Run: resumed, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil || raw != `["job_1","job_2"]` {
		t.Fatalf("resumed run = %s, %v", raw, err)
	}
	if got := fake.JobCount(); got != 2 {
		t.Fatalf("stored jobs = %d, want the original 2", got)
	}
	if got := len(fake.JobStarts()); got != 2 {
		t.Fatalf("RunJob calls = %d, want no replay submissions", got)
	}
}

func TestJobRecoversSubmissionWithoutJournalID(t *testing.T) {
	script := `return await job({command: ['make', 'test']})`
	source, err := runstore.Create("job lost response", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &enginefake.Engine{JobSubmitErrors: []error{errors.New("response lost")}}
	if _, _, err := runCommandScript(t, script, Options{Run: source, Engine: fake, JobNamespace: source.Meta.ID}); err == nil {
		t.Fatal("the lost submission response did not fail the first run")
	}
	entries, err := runstore.ReadJournal(source.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.State != "error" || last.Job != "" {
		t.Fatalf("first journal = %+v, want a final error without a job ID", entries)
	}
	resumed, err := runstore.Create("job recovered response", script, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := runCommandScript(t, script, Options{Run: resumed, Engine: fake, Cache: NewCache(entries), JobNamespace: source.Meta.ID})
	if err != nil {
		t.Fatalf("resume could not recover the original job: %v", err)
	}
	var result jobResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "job_1" || fake.JobCount() != 1 {
		t.Fatalf("recovered result = %+v, stored jobs = %d; want the original job only", result, fake.JobCount())
	}
	starts := fake.JobStarts()
	if len(starts) != 2 || starts[0].IdempotencyKey == "" || starts[0].IdempotencyKey != starts[1].IdempotencyKey {
		t.Fatalf("submission keys = %+v, want one stable key", starts)
	}
}
