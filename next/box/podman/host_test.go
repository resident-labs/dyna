package podman

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

type runner struct {
	calls        [][]string
	dead         bool
	fail         bool
	missingFrame bool
}

func (r *runner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	for i, a := range args {
		if a == "-O" && i+1 < len(args) {
			if args[i+1] == "check" && r.dead {
				return nil, errors.New("dead")
			}
			return nil, nil
		}
		if a == "-M" {
			r.dead = false
			return nil, nil
		}
	}
	if r.fail {
		return nil, errors.New("lost")
	}
	if r.missingFrame {
		return []byte("partial"), nil
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}
func newTestHost(t *testing.T, r *runner) *Host {
	t.Helper()
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestCapacityReservesFloorWithoutCountingBoxesTwice(t *testing.T) {
	cpus, memory, disk, err := parseCapacity([]byte("8 16000 5000 90000"), 4000, 20000)
	if err != nil || cpus != 7 || memory != 12000 || disk != 90000 {
		t.Fatalf("capacity %d %d %d: %v", cpus, memory, disk, err)
	}
	if _, _, _, err := parseCapacity([]byte("8 16000 3999 90000"), 4000, 20000); err == nil {
		t.Fatal("accepted memory below floor")
	}
}

func TestTrailerRejectsSuccessfulSSHWithoutFrame(t *testing.T) {
	for _, s := range []string{"", "ok", "xDYNA-END 00000000000000000000 000\n", "DYNA-END 00000000000000000000 999\n"} {
		if _, _, err := parseReply([]byte(s)); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	h := newTestHost(t, &runner{missingFrame: true})
	if err := h.StartContainer(context.Background(), "a"); err == nil {
		t.Fatal("accepted ssh success without a trailer")
	}
	out, status, err := parseReply([]byte("okDYNA-END 00000000000000000002 007\n"))
	if err != nil || string(out) != "ok" || status != 7 {
		t.Fatalf("reply %q %d %v", out, status, err)
	}
}
func TestContainerAndGrandchildSliceArguments(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	ctx := context.Background()
	if err := h.EnsureSlice(ctx, box.Slice{Name: "dyna-aaa-bbb-ccc.slice", CPUs: 2, MemHigh: 4, MemMax: 6, Pids: 8}); err != nil {
		t.Fatal(err)
	}
	if err := h.CreateContainer(ctx, box.ContainerSpec{Box: "a", Image: "image", Slice: "dyna-aaa-bbb-ccc.slice", Mounts: []box.Mount{{HostPath: "/data", BoxPath: "/box"}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call   int
		suffix string
	}{
		{3, "'systemctl' '--user' 'set-property' '--runtime' 'dyna-aaa-bbb-ccc.slice' 'CPUQuota=200%' 'MemoryHigh=4' 'MemoryMax=6' 'TasksMax=8'"},
		{5, "'podman' '--cgroup-manager=systemd' 'create' '--name' 'a' '--label' 'dyna.box=a' '--cgroup-parent=dyna-aaa-bbb-ccc.slice' '--cgroupns=private' '--systemd=always' '--stop-signal' 'SIGTERM' '--entrypoint' '/usr/bin/tini' '--tmpfs' '/run/dyna' '--env' 'HOME=/box/home' '--env' 'PI_CODING_AGENT_DIR=/box/pi' '--volume' '/data:/box' 'image' '--' '/opt/dyna/bin/current/dyna' 'box-agent'"},
	} {
		args := r.calls[tc.call]
		if !strings.HasSuffix(args[len(args)-1], tc.suffix) {
			t.Fatalf("command %q", args[len(args)-1])
		}
	}
}
func TestForwardRestoredAfterMasterDies(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	ctx := context.Background()
	f := box.Forward{HostPath: "/remote/s", EnginePath: "/local/s"}
	if err := h.AddForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	r.dead = true
	if err := h.StartContainer(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, args := range r.calls {
		for i, a := range args {
			if a == "-O" {
				ops = append(ops, args[i+1])
			}
		}
	}
	if !reflect.DeepEqual(ops, []string{"check", "forward", "check", "cancel"}) {
		t.Fatalf("operations %v", ops)
	}
	for _, args := range r.calls {
		for i, a := range args {
			if a != "-O" || (args[i+1] != "forward" && args[i+1] != "cancel") {
				continue
			}
			want := append(h.base(), "-O", args[i+1], "-R", "/remote/s:/local/s", "user@host")
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("forward argv %v, want %v", args, want)
			}
		}
	}
}

type ticker struct{ ch chan time.Time }

func (t ticker) C() <-chan time.Time { return t.ch }
func (ticker) Stop()                 {}
func TestHeartbeatThreeMisses(t *testing.T) {
	r := &runner{fail: true}
	h := newTestHost(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := ticker{make(chan time.Time)}
	reports := make(chan bool)
	done := make(chan struct{})
	go func() { defer close(done); h.Heartbeat(ctx, ticks, func(ok bool) { reports <- ok }) }()
	for i := 0; i < 3; i++ {
		ticks.ch <- time.Time{}
	}
	if <-reports {
		t.Fatal("expected unreachable")
	}
	cancel()
	<-done
}
