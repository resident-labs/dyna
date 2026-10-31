package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

type pendingStore struct{ boxStore }

func (pendingStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, State: contract.BoxPending}, nil
}

func TestRemoveUnplacedBox(t *testing.T) {
	b := &Boxes{Store: pendingStore{}}
	if err := b.Remove(context.Background(), "box_1"); err != nil {
		t.Fatal(err)
	}
}

type missingContainerRunner struct{ stopSeen bool }

func (r *missingContainerRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	status := 0
	if strings.Contains(command, "'podman' '--cgroup-manager=systemd' 'stop'") {
		r.stopSeen = true
		if !strings.Contains(command, "'--ignore'") {
			status = 125
		}
	}
	if strings.Contains(command, "'volume' 'rm'") {
		status = 1
	}
	return []byte(fmt.Sprintf("DYNA-END %020d %03d\n", 0, status)), nil
}

func TestRemoveHostBoxWithMissingContainer(t *testing.T) {
	runner := &missingContainerRunner{}
	host, err := podman.New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	record := box.Box{ID: "box_1", Slice: "dyna-test.slice"}
	forward := box.Forward{Box: record.ID, HostPath: "/host/sock/box_1/engine.sock", EnginePath: "/runtime/boxes/box_1.sock"}
	if err := removeHostBox(context.Background(), host, record, forward); err != nil {
		t.Fatal(err)
	}
	if !runner.stopSeen {
		t.Fatal("cleanup did not stop the container")
	}
}

func TestRemoveHostBoxCleansPartialCreation(t *testing.T) {
	for _, stage := range []string{"before slice", "after slice", "after forward", "after container"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			host := fakehost.New()
			record := box.Box{ID: "box_1", Slice: "dyna-test.slice"}
			forward := box.Forward{Box: record.ID, HostPath: "/host/sock/box_1/engine.sock", EnginePath: "/runtime/boxes/box_1.sock"}
			if stage != "before slice" {
				if err := host.EnsureSlice(ctx, box.Slice{Name: record.Slice}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after forward" || stage == "after container" {
				if err := host.AddForward(ctx, forward); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after container" {
				if err := host.CreateContainer(ctx, box.ContainerSpec{Box: record.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeHostBox(ctx, host, record, forward); err != nil {
				t.Fatal(err)
			}
			if len(host.Slices) != 0 || len(host.Forwards) != 0 || len(host.Containers) != 0 || len(host.Volumes) != 0 {
				t.Fatalf("resources remain: %+v", host)
			}
		})
	}
}

// TestHostLoadControlPlaneCPUAndMemory reads two /proc/stat samples and one /proc/meminfo from a temp folder.
func TestHostLoadControlPlaneCPUAndMemory(t *testing.T) {
	proc := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(proc, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("meminfo", "MemTotal:       8388608 kB\nMemFree:        1024 kB\nMemAvailable:   2097152 kB\n")
	b := &Boxes{Store: listStore{}, Proc: proc}
	write("stat", "cpu  100 0 100 800 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0\n")
	hosts := []contract.HostSummary{{Name: contract.ControlPlane}, {Name: "host"}}
	if err := b.HostLoad(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	write("stat", "cpu  200 0 200 1000 0 0 0 0\ncpu0 100 0 100 500 0 0 0 0\n")
	if err := b.HostLoad(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	cp := hosts[0]
	wantCPU := 0.5 * float64(goruntime.NumCPU())
	if !cp.Reachable || cp.CPUs != goruntime.NumCPU() || cp.CPUUsed != wantCPU || cp.MemBytes != 8388608*1024 || cp.MemUsed != 6291456*1024 {
		t.Fatalf("control plane: %+v, want cores %v", cp, wantCPU)
	}
}
