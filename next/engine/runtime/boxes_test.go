package runtime

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
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

func TestControlPlaneParsers(t *testing.T) {
	b := &Boxes{}
	b.cpuCores("cpu 100 0 100 800 0 0 0 0", 8)
	cpu := b.cpuCores("cpu 200 0 200 1000 0 0 0 0", 8)
	total, used := memory("MemTotal: 8388608 kB\nMemAvailable: 2097152 kB\n")
	if cpu != 4 || total != 8388608*1024 || used != 6291456*1024 {
		t.Fatalf("cpu %v memory %d/%d", cpu, used, total)
	}
}

func TestRecordHostLoad(t *testing.T) {
	b := &Boxes{Store: listStore{}}
	start := time.Now().Truncate(time.Minute)
	b.record("host", true, podman.Load{CPUUsed: 1, MemUsed: 10, DiskBytes: 100, DiskUsed: 20}, start)
	b.record("host", true, podman.Load{CPUUsed: 3, MemUsed: 20, DiskBytes: 100, DiskUsed: 30}, start.Add(10*time.Second))
	b.record("host", true, podman.Load{CPUUsed: 4, MemUsed: 40, DiskBytes: 100, DiskUsed: 50}, start.Add(70*time.Second))
	b.record("host", false, podman.Load{}, start.Add(80*time.Second))
	hosts := []contract.HostSummary{{Name: "host"}}
	if err := b.HostLoad(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	got := hosts[0]
	want := []contract.HostLoadPoint{{At: start, CPUUsed: 2, MemUsed: 15}, {At: start.Add(time.Minute), CPUUsed: 4, MemUsed: 40}}
	if got.Reachable || got.CPUUsed != 4 || got.MemUsed != 40 || got.DiskBytes != 100 || got.DiskUsed != 50 || !got.LastSeen.Equal(start.Add(70*time.Second)) || !reflect.DeepEqual(got.History, want) {
		t.Fatalf("load = %+v", got)
	}
	b.record("host", true, podman.Load{}, start.Add(61*time.Minute))
	if got := b.loads["host"].summary.History; len(got) != 1 {
		t.Fatalf("old points retained: %+v", got)
	}
}

type childSliceRunner struct{ childRunning bool }

func (r *childSliceRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	if strings.Contains(command, "'systemctl' '--user' 'stop'") && strings.Contains(command, "dyna-parent.slice") {
		r.childRunning = false
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestLegacyParentWakeKeepsRunningChild(t *testing.T) {
	runner := &childSliceRunner{childRunning: true}
	host, err := podman.New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}}
	spec := lifecycle.BoxSpec{Box: "parent", Host: "host", Slice: "dyna-box_parent.slice"}
	if err := b.Recreate(context.Background(), spec, "new-image", "dyna-parent.slice"); err != nil {
		t.Fatal(err)
	}
	if !runner.childRunning {
		t.Fatal("image refresh stopped the running child slice")
	}
}

type blockedHostsStore struct {
	boxStore
	entered, release chan struct{}
}

func (s blockedHostsStore) Hosts(context.Context) ([]box.Host, error) {
	close(s.entered)
	<-s.release
	return nil, nil
}
func TestHostLookupDoesNotLockHeartbeat(t *testing.T) {
	s := blockedHostsStore{entered: make(chan struct{}), release: make(chan struct{})}
	b := &Boxes{Store: s}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = b.host(context.Background(), "slow") }()
	<-s.entered
	locked := b.mu.TryLock()
	if locked {
		b.mu.Unlock()
	}
	close(s.release)
	<-done
	if !locked {
		t.Fatal("host lookup holds heartbeat lock during store I/O")
	}
}
