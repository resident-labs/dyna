package podman

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type limitRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r *limitRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	if strings.Contains(command, "'short'") {
		r.entered <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

// maxSessionsRunner fails a command past sshd's default MaxSessions on the master. A command with no control socket is a
// connection of its own and takes none of them.
type maxSessionsRunner struct {
	mu          sync.Mutex
	active      int
	direct      int
	misrouted   []string
	entered     chan string
	release     chan struct{}
	stopRelease chan struct{}
}

func (r *maxSessionsRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	master := slices.Contains(args, "-S")
	exec := strings.Contains(command, "'dyna-hostexec'")
	r.mu.Lock()
	if exec == master {
		r.misrouted = append(r.misrouted, command)
	}
	if master {
		if r.active >= 10 {
			r.mu.Unlock()
			return nil, errors.New("MaxSessions exceeded")
		}
		r.active++
	} else {
		r.direct++
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if master {
			r.active--
		} else {
			r.direct--
		}
		r.mu.Unlock()
	}()
	kind := ""
	switch {
	case exec:
		kind = "exec"
	case strings.Contains(command, "'short'"):
		kind = "short"
	case strings.Contains(command, "'systemctl' '--user' 'stop'"):
		kind = "stop"
	}
	if kind != "" {
		r.entered <- kind
		release := r.release
		if kind == "stop" {
			release = r.stopRelease
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestHostexecLeavesTheMastersSessions(t *testing.T) {
	const runs = 20 // twice MaxSessions, so they could not all fit on the master
	r := &maxSessionsRunner{entered: make(chan string, runs+8), release: make(chan struct{}), stopRelease: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	h.dialSetup = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, runs+5)
	for range runs {
		go func() {
			_, _, _, err := h.RunHostexec(ctx, contract.HostexecRunArgs{Argv: []string{"true"}}, "op")
			done <- err
		}()
	}
	for range runs {
		if kind := <-r.entered; kind != "exec" {
			t.Fatalf("got %s", kind)
		}
	}
	r.mu.Lock()
	active, direct := r.active, r.direct
	r.mu.Unlock()
	if active != 0 || direct != runs {
		t.Fatalf("%d on the master, %d direct", active, direct)
	}
	// With every host run going, the engine still has all of the master's budget.
	cancelled, stop := context.WithCancel(ctx)
	stop()
	for range 5 {
		go func() { _, err := h.run(ctx, "short"); done <- err }()
	}
	for range 5 {
		if kind := <-r.entered; kind != "short" {
			t.Fatalf("got %s", kind)
		}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- h.StopHostexec(ctx, "scope") }()
	if kind := <-r.entered; kind != "stop" {
		t.Fatalf("got %s", kind)
	}
	if err := h.StopHostexec(cancelled, "scope2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second stop: %v", err)
	}
	ticks := make(chan time.Time, 1)
	reports := make(chan bool, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, _ Load) { reports <- ok }, func(string, contract.PressureKind) {})
	}()
	ticks <- time.Time{}
	if !<-reports {
		t.Fatal("heartbeat could not use reserved SSH session")
	}
	close(r.stopRelease)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	close(r.release)
	for range runs + 5 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	<-heartbeatDone
	if len(r.misrouted) > 0 {
		t.Fatalf("on the wrong connection: %v", r.misrouted)
	}
}

// dialRunner holds each command until it is released, one per value sent.
type dialRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r *dialRunner) Run(ctx context.Context, _ []byte, _ ...string) ([]byte, error) {
	r.entered <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

func TestHostexecBoundsConnectionsBeingOpened(t *testing.T) {
	r := &dialRunner{entered: make(chan struct{}, 16), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	h.dialSetup = time.Hour // no handshake ends, so only a finished command frees a slot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, hostexecDialing+2)
	for range hostexecDialing + 1 {
		go func() {
			_, _, _, err := h.RunHostexec(ctx, contract.HostexecRunArgs{Argv: []string{"true"}}, "op")
			done <- err
		}()
	}
	for range hostexecDialing {
		<-r.entered
	}
	select {
	case <-r.entered:
		t.Fatal("a fifth connection opened while four were setting up")
	case <-time.After(50 * time.Millisecond):
	}
	// One waiting for a slot gives up at its deadline.
	late, stop := context.WithCancel(ctx)
	stop()
	if _, _, _, err := h.RunHostexec(late, contract.HostexecRunArgs{Argv: []string{"true"}}, "late"); !errors.Is(err, context.Canceled) {
		t.Fatalf("late run: %v", err)
	}
	r.release <- struct{}{}
	<-r.entered
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Once its handshake's time has passed, a running command holds no slot, so running commands have no limit.
	quick, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	quick.dialSetup = time.Millisecond
	for range 3 * hostexecDialing {
		go func() { _, _, _, _ = quick.RunHostexec(ctx, contract.HostexecRunArgs{Argv: []string{"true"}}, "op") }()
	}
	for range 3 * hostexecDialing {
		<-r.entered
	}
}

func TestHostexecConnectionSharesNoMaster(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	h.TrustNewKey()
	if _, _, _, err := h.RunHostexec(context.Background(), contract.HostexecRunArgs{Argv: []string{"true"}, Timeout: 1000}, "op"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls %v", r.calls)
	}
	call := r.calls[0]
	joined := strings.Join(call, " ")
	if slices.Contains(call, "-S") || slices.Contains(call, "-M") || !strings.Contains(joined, "ControlMaster=no") || !strings.Contains(joined, "ControlPath=none") {
		t.Fatalf("not a connection of its own: %v", call)
	}
	// A pool machine's own key list applies to it as it does to the master.
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") || !strings.Contains(joined, "UserKnownHostsFile="+h.socket+".known") || !strings.Contains(joined, " user@host ") {
		t.Fatalf("%v", call)
	}
}

func TestShortCommandsLeaveHeartbeatAndHostexecUnblocked(t *testing.T) {
	r := &limitRunner{entered: make(chan struct{}, 9), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 5)
	for range 5 {
		go func() { _, _ = h.run(ctx, "short"); done <- struct{}{} }()
	}
	for range 5 {
		<-r.entered
	}
	// A sixth short command must wait for a slot, and cancellation must stop it.
	sixth, stop := context.WithCancel(ctx)
	stop()
	if _, err := h.run(sixth, "short"); !errors.Is(err, context.Canceled) {
		t.Fatalf("sixth command: %v", err)
	}
	select {
	case <-r.entered:
		t.Fatal("sixth command reached SSH")
	default:
	}
	ticks := make(chan time.Time, 1)
	reports := make(chan bool, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, _ Load) { reports <- ok }, func(string, contract.PressureKind) {})
	}()
	ticks <- time.Time{}
	if !<-reports {
		t.Fatal("heartbeat failed while short commands were busy")
	}
	if err := h.StopHostexec(ctx, "scope"); err != nil {
		t.Fatalf("hostexec stop: %v", err)
	}
	close(r.release)
	for range 5 {
		<-done
	}
	cancel()
	<-heartbeatDone
}
