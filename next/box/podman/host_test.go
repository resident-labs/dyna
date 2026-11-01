package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type runner struct {
	calls        [][]string
	dead         bool
	fail         bool
	missingFrame bool
	body         string
	status       int
}

func (r *runner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for i, a := range args {
		if a == "-O" && i+1 < len(args) && args[i+1] == "exit" {
			return nil, nil
		}
	}
	r.calls = append(r.calls, append([]string(nil), args...))
	for i, a := range args {
		if a == "-O" && i+1 < len(args) {
			if args[i+1] == "check" && r.dead {
				return nil, errors.New("dead")
			}
			return nil, nil
		}
		if a == "-M" {
			// Like ssh, a new master keeps an existing control socket and then serves none.
			socket := args[slices.Index(args, "-S")+1]
			if _, err := os.Lstat(socket); err == nil {
				return nil, nil
			}
			r.dead = false
			return nil, os.WriteFile(socket, nil, 0600)
		}
	}
	if r.fail {
		return nil, errors.New("lost")
	}
	if r.missingFrame {
		return []byte("partial"), nil
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", r.body, len(r.body), r.status)), nil
}
func newTestHost(t *testing.T, r *runner) *Host {
	t.Helper()
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestListContainersMarksOldLayouts(t *testing.T) {
	r := &runner{body: `[{"Labels":{"dyna.box":"current","dyna.layout":"` + box.ContainerLayout + `"},"State":"running"},{"Labels":{"dyna.box":"old","dyna.shared":"2"},"State":"exited"},{"Labels":{"dyna.box":"missing"},"State":"exited"}]`}
	got, err := newTestHost(t, r).ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].StaleLayout || !got[1].StaleLayout || !got[2].StaleLayout {
		t.Fatalf("layout status: %+v", got)
	}
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

func TestCapacityCreatesHostDataBeforeDiskProbe(t *testing.T) {
	r := &runner{body: "8 16000 5000 90000"}
	h := newTestHost(t, r)
	_, _, _, err := h.capacity(context.Background(), CheckOptions{HostData: "/host/data", MemoryFloor: 4000, DiskFloor: 20000})
	if err != nil {
		t.Fatal(err)
	}
	args := r.calls[1]
	cmd := args[len(args)-1]
	if !strings.Contains(cmd, `mkdir -p -m 0700 -- "$1"; getconf`) || !strings.HasSuffix(cmd, "'probe' '/host/data'") {
		t.Fatalf("capacity command %q", cmd)
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
	if err := h.CreateContainer(ctx, box.ContainerSpec{Box: "a", Image: "image", Slice: "dyna-aaa-bbb-ccc.slice", Mounts: []box.Mount{{HostPath: "/host/sock/a", BoxPath: "/run/dyna/engine", ReadOnly: true}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call   int
		suffix string
	}{
		{3, "'systemctl' '--user' 'set-property' '--runtime' 'dyna-aaa-bbb-ccc.slice' 'CPUQuota=200%' 'MemoryHigh=4' 'MemoryMax=6' 'TasksMax=8'"},
		{5, "'podman' '--cgroup-manager=systemd' 'create' '--name' 'a' '--userns=keep-id:uid=1000,gid=1000' '--user' '1000:1000' '--label' 'dyna.box=a' '--cgroup-parent=dyna-aaa-bbb-ccc.slice' '--cgroupns=private' '--systemd=always' '--stop-signal' 'SIGTERM' '--entrypoint' '/usr/bin/tini' '--tmpfs' '/run/dyna:mode=1777' '--env' 'HOME=/box/home' '--env' 'PI_CODING_AGENT_DIR=/box/pi' '--volume' 'dyna-box-a:/box' '--volume' '/host/sock/a:/run/dyna/engine:ro' 'image' '--' '/opt/dyna/bin/current/dyna' 'box-agent'"},
	} {
		args := r.calls[tc.call]
		if !strings.HasSuffix(args[len(args)-1], tc.suffix) {
			t.Fatalf("command %q", args[len(args)-1])
		}
	}
}
func TestNestedContainerNeeds(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "a", Image: "image", Needs: []box.Need{box.NeedNestedContainers}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := r.calls[len(r.calls)-1][len(r.calls[len(r.calls)-1])-1]
	if !strings.Contains(cmd, "test -c /dev/net/tun") || !strings.Contains(cmd, "'--device' '/dev/net/tun' '--security-opt' 'unmask=/proc/sys' 'image'") || strings.Contains(cmd, "/dev/kvm") {
		t.Fatalf("command %s", cmd)
	}
}

func TestMissingDeviceIsTyped(t *testing.T) {
	r := &runner{status: 254, body: "kvm"}
	h := newTestHost(t, r)
	err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "a", Image: "image", Needs: []box.Need{box.NeedKVM}})
	var missing box.MissingDevice
	if !errors.As(err, &missing) || missing.Need != box.NeedKVM {
		t.Fatalf("error %v", err)
	}
	cmd := r.calls[len(r.calls)-1][len(r.calls[len(r.calls)-1])-1]
	if !strings.Contains(cmd, "'--device' '/dev/kvm' '--group-add' 'keep-groups' 'image'") {
		t.Fatalf("KVM command %s", cmd)
	}
}

func TestStopContainerIgnoresMissing(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	if err := h.StopContainer(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	args := r.calls[1]
	if !strings.HasSuffix(args[len(args)-1], "'podman' '--cgroup-manager=systemd' 'stop' '--ignore' 'a'") {
		t.Fatalf("command %v", args)
	}
}
func TestRemoveContainerLeavesVolumeForMove(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	if err := h.RemoveContainer(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || !strings.HasSuffix(r.calls[1][len(r.calls[1])-1], "'podman' '--cgroup-manager=systemd' 'rm' '--ignore' '--volumes' 'a'") {
		t.Fatalf("commands %v", r.calls)
	}
}
func TestRemoveVolumeAcceptsMissingVolume(t *testing.T) {
	r := &runner{status: 1}
	h := newTestHost(t, r)
	if err := h.RemoveVolume(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	args := r.calls[1]
	if !strings.HasSuffix(args[len(args)-1], "'podman' '--cgroup-manager=systemd' 'volume' 'rm' 'dyna-box-a'") {
		t.Fatalf("command %v", args)
	}
}

func TestForwardRestoredAfterMasterDies(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	ctx := context.Background()
	f := box.Forward{Box: "a", HostPath: "/remote/sock/a/engine.sock", EnginePath: "/local/s"}
	if err := h.AddForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := h.CreateContainer(ctx, box.ContainerSpec{Box: "a", Image: "image", Slice: "dyna-a.slice", Mounts: []box.Mount{{HostPath: "/remote/sock/a", BoxPath: "/run/dyna/engine", ReadOnly: true}}}); err != nil {
		t.Fatal(err)
	}
	create := r.calls[len(r.calls)-1]
	if !strings.Contains(create[len(create)-1], "'/remote/sock/a:/run/dyna/engine:ro'") {
		t.Fatalf("socket directory mount missing: %v", create)
	}
	r.calls = nil
	r.dead = true
	if err := h.StartContainer(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	// The box mounts /remote/sock/a as a directory, so replacing engine.sock
	// during restoration does not require replacing the container mount.
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
			want := append(h.base(), "-O", args[i+1], "-R", "/remote/sock/a/engine.sock:/local/s", "user@host")
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("forward argv %v, want %v", args, want)
			}
		}
	}
}

// After an engine restart or a dead master, the old box socket is still on the host, and sshd
// refuses to forward over it. Each forward removes it first.
func TestForwardRemovesStaleHostSocketFirst(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	ctx := context.Background()
	f := box.Forward{Box: "a", HostPath: "/remote/sock/a/engine.sock", EnginePath: "/local/s"}
	removedBeforeForward := func(stage, remove string) {
		for i, args := range r.calls {
			if len(args) > 4 && args[len(args)-4] == "forward" {
				previous := r.calls[i-1]
				if !strings.HasSuffix(previous[len(previous)-1], remove) {
					t.Fatalf("%s: the forward did not remove the old socket first", stage)
				}
				return
			}
		}
		t.Fatalf("%s: no forward in %v", stage, r.calls)
	}
	if err := h.AddForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	removedBeforeForward("add", `rm -f -- "$2"' 'dyna' '/remote/sock/a' '/remote/sock/a/engine.sock'`)
	r.calls = nil
	r.dead = true
	if err := h.StartContainer(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	removedBeforeForward("restore", "'rm' '-f' '--' '/remote/sock/a/engine.sock'")
}

// A master killed with SIGKILL leaves its control socket. Each heartbeat then started one more
// master that served no socket, and every host command failed.
func TestRestartRemovesDeadMasterSocket(t *testing.T) {
	r := &runner{dead: true}
	h := newTestHost(t, r)
	if err := os.WriteFile(h.socket, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.StartContainer(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if r.dead {
		t.Fatal("the new master did not serve the control socket")
	}
}

type resetRunner struct{ calls [][]string }

func (r *resetRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	for i, arg := range args {
		if arg == "-O" && args[i+1] == "exit" {
			return nil, nil
		}
		if arg == "-O" && args[i+1] == "check" {
			return nil, errors.New("no master")
		}
		if arg == "-M" {
			return nil, nil
		}
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}
func TestFirstUseReplacesStaleMaster(t *testing.T) {
	r := &resetRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.StartContainer(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	want := append(h.base(), "-O", "exit", "user@host")
	if !reflect.DeepEqual(r.calls[0], want) || !reflect.DeepEqual(r.calls[2], []string{"ssh", "-M", "-N", "-f", "-S", h.socket, "-o", "ControlMaster=yes", "-o", "ControlPersist=10m", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-o", "StreamLocalBindUnlink=yes", "user@host"}) {
		t.Fatalf("commands %v", r.calls)
	}
}

type blockingRunner struct {
	count                    atomic.Int32
	entered, release, second chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-O" {
			return nil, nil
		}
	}
	if r.count.Add(1) == 1 {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		close(r.second)
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}
func TestHeartbeatRunsDuringHostCommand(t *testing.T) {
	r := &blockingRunner{entered: make(chan struct{}), release: make(chan struct{}), second: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = h.run(ctx, "long") }()
	<-r.entered
	// The first command is paused at the SSH boundary. Verify that host state
	// is available before starting a probe, and cancel to unblock a regression.
	select {
	case h.gate <- struct{}{}:
		<-h.gate
	default:
		cancel()
		<-done
		t.Fatal("heartbeat blocked by host command")
	}
	probeDone := make(chan struct{})
	go func() { defer close(probeDone); _, _ = h.run(context.Background(), "true") }()
	<-r.second
	<-probeDone
	cancel()
	<-done
}
func TestCancelledHeartbeatDoesNotWaitForHostState(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	if err := h.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.run(ctx, "true"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestHeartbeatThreeMisses(t *testing.T) {
	r := &runner{fail: true}
	h := newTestHost(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	reports := make(chan bool)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, "/data", func(ok bool, _ Load) { reports <- ok }, func(string, contract.PressureKind) {})
	}()
	for i := 0; i < 3; i++ {
		ticks <- time.Time{}
	}
	if <-reports {
		t.Fatal("expected unreachable")
	}
	ticks <- time.Time{}
	if <-reports {
		t.Fatal("expected another unreachable report")
	}
	cancel()
	<-done
}
