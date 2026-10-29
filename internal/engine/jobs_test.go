package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/room"
)

func TestJobEventDescribesResults(t *testing.T) {
	exit := func(code int) *int { return &code }
	tests := []struct {
		name  string
		state jobs.State
		exit  *int
		note  string
		want  string
	}{
		{name: "passed", state: jobs.StatePassed, exit: exit(0), want: "passed with exit code 0"},
		{name: "failed", state: jobs.StateFailed, exit: exit(2), want: "failed with exit code 2"},
		{name: "idle timeout", state: jobs.StateTimedOut, note: "no output for 30s", want: "timed out: no output for 30s"},
		{name: "cancelled", state: jobs.StateCancelled, note: "cancelled", want: "was cancelled"},
		{name: "lost", state: jobs.StateLost, note: "node pve disappeared", want: "was lost: node pve disappeared"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := (jobsReporter{}).event(jobs.Job{ID: "job_test", Node: "pve", Command: []string{"make", "test"}, State: tc.state, ExitCode: tc.exit, Note: tc.note})
			post, ok := room.DescribeEvent(ev)
			line, _, _ := strings.Cut(post.Text, "\n")
			want := "Job make test on pve " + tc.want + "."
			if !ok || line != want {
				t.Fatalf("notice = %q, %v; want %q", line, ok, want)
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Contains(string(payload), `"exitCode"`), tc.exit != nil; got != want {
				t.Fatalf("payload exit code presence = %v, want %v: %s", got, want, payload)
			}
		})
	}
}

func TestWaitJobReturnsAfterJobsEvent(t *testing.T) {
	store := jobs.Store{DataDir: t.TempDir()}
	queued := jobs.Job{ID: "job_wait", Command: []string{"true"}, State: jobs.StateQueued}
	if err := store.Save(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	events := hub.New(1)
	s := services{jobs: &jobs.Service{Store: store}, events: events}
	type answer struct {
		result jobs.WaitResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := s.waitJob(t.Context(), queued.ID)
		done <- answer{result: result, err: err}
	}()
	code := 0
	finished := queued
	finished.State, finished.ExitCode = jobs.StatePassed, &code
	if err := store.Save(t.Context(), finished); err != nil {
		t.Fatal(err)
	}
	events.Publish(hub.Event{Topic: jobs.Topic})
	got := <-done
	if got.err != nil || !got.result.Done || got.result.Job.State != jobs.StatePassed || got.result.Job.ExitCode == nil || *got.result.Job.ExitCode != 0 {
		t.Fatalf("WaitJob = %+v, %v", got.result, got.err)
	}
}

func TestCancelJobAllowsOnlyOwningWorkflowRun(t *testing.T) {
	store := jobs.Store{DataDir: t.TempDir()}
	s := services{jobs: &jobs.Service{Store: store}}
	job := jobs.Job{ID: "job_owned", Run: "wf_one", Session: "ses_one", Command: []string{"true"}, State: jobs.StateQueued}
	if err := store.Save(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Worker, Run: "wf_one"})
	got, err := s.cancelJob(owner, job.ID)
	if err != nil || !got.CancelRequested {
		t.Fatalf("owning run cancel = %+v, %v", got, err)
	}
	other := authority.WithActor(t.Context(), authority.Actor{Role: authority.Worker, Run: "wf_two"})
	if _, err := s.cancelJob(other, job.ID); err == nil {
		t.Fatal("another workflow run cancelled the job")
	}
	leaf := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "ses_two", Run: "wf_one"})
	if _, err := s.cancelJob(leaf, job.ID); err == nil {
		t.Fatal("another session in the workflow run cancelled the job")
	}
}

type jobMachineSource struct{ local machines.Machine }

func (s jobMachineSource) Get(_ context.Context, name string) (machines.Machine, error) {
	if name == s.local.Name {
		return s.local, nil
	}
	return machines.Machine{}, os.ErrNotExist
}

func (s jobMachineSource) GetLocal(context.Context) (machines.Machine, error) {
	return s.local, nil
}

func (s jobMachineSource) List(context.Context) ([]machines.Machine, error) {
	return []machines.Machine{s.local}, nil
}

// The RPC boundary validates names, removes repeat requests, and stores the
// typed result without starting an engine or a node process.
func TestRunJobValidatesAndStoresResources(t *testing.T) {
	dir := t.TempDir()
	node := machines.Machine{Name: "local", Enabled: true, Local: true}
	store := jobs.Store{DataDir: dir}
	s := services{jobs: &jobs.Service{
		Store: store, Machines: jobMachineSource{local: node}, DataDir: dir,
	}}
	job, err := s.runJob(context.Background(), runJobInput{
		Command: []string{"true"}, Host: true,
		Resources: []string{"ghidra", "display", "ghidra"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []machines.ResourceName{"display", "ghidra"}
	if !slices.Equal(stored.Resources, want) {
		t.Fatalf("stored resources = %v, want %v", stored.Resources, want)
	}
	for _, bad := range []string{"", "Display", "bad/name", strings.Repeat("a", 65)} {
		if _, err := s.runJob(context.Background(), runJobInput{
			Command: []string{"true"}, Host: true, Resources: []string{bad},
		}); err == nil {
			t.Fatalf("resource name %q was accepted", bad)
		}
	}
}
