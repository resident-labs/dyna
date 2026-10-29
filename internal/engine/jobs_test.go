package engine

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
)

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
