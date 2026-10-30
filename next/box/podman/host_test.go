package podman

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

type runner struct {
	calls [][]string
	dead  bool
	fail  bool
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
func TestTrailerRejectsSuccessfulSSHWithoutFrame(t *testing.T) {
	for _, s := range []string{"", "ok", "xDYNA-END 00000000000000000000 000\n", "DYNA-END 00000000000000000000 999\n"} {
		if _, _, err := parseReply([]byte(s)); err == nil {
			t.Fatalf("accepted %q", s)
		}
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
	all := fmt.Sprint(r.calls)
	for _, want := range []string{"'CPUQuota=200%'", "'MemoryHigh=4'", "'MemoryMax=6'", "'TasksMax=8'", "'--cgroup-parent=dyna-aaa-bbb-ccc.slice'", "'--cgroupns=private'", "'dyna.box=a'", "'/data:/box'"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %s in %s", want, all)
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
			if a == "-R" && args[i+1] != "/remote/s:/local/s" {
				t.Fatalf("forward %v", args)
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
