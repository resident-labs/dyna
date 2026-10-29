package engine

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

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
