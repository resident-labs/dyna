package watch

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
)

func TestReadyCINodesUsesFirstCheckDemand(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	registry := machines.Store{DataDir: dir}
	var nodes []localci.Node
	for _, name := range []string{"busy", "idle"} {
		m, err := registry.Save(ctx, machines.Machine{Name: name, SSH: name, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = registry.SaveProbe(ctx, m.ID, machines.ProbeResult{Health: machines.HealthOnline, Capabilities: machines.Capabilities{CPUs: 8, MemTotal: 8 << 30}})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, localci.Node{Name: name, Enabled: true, CPUs: 8, MemoryBytes: 8 << 30})
	}
	queue := jobs.Admission{DataDir: dir, Engine: true}
	if _, err := queue.Hold(ctx, jobs.Demand{ID: "held", Node: "busy", CPUs: 1, MemoryMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	definition, err := localci.ParseDefinition([]byte("version: 1\ntrial: [test]\nchecks:\n  - id: test\n    image: alpine\n    command: true\n    cpus: 2\n    memory: 6g\nsuites:\n  - id: test\n    checks: [test]\n"))
	if err != nil {
		t.Fatal(err)
	}
	service := Service{Store: &Store{Root: filepath.Join(dir, "watch")}}
	ready := service.readyCINodes(ctx, nodes, registeredCIJob{record: Record{RoomID: "room"}}, localCIPlan{execution: definition, suites: []localci.Suite{"test"}})
	if len(ready) != 1 || ready[0].Name != "idle" {
		t.Fatalf("ready nodes = %+v, want idle", ready)
	}
	if _, err := queue.Hold(ctx, jobs.Demand{ID: "other", Node: "idle", CPUs: 1, MemoryMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	ready = service.readyCINodes(ctx, nodes, registeredCIJob{record: Record{RoomID: "room"}}, localCIPlan{execution: definition, suites: []localci.Suite{"test"}})
	if len(ready) != 0 {
		t.Fatalf("full nodes were ready: %+v", ready)
	}
}
