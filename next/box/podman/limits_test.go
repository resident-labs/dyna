package podman

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/hostexec"
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

type maxSessionsRunner struct {
	mu          sync.Mutex
	active      int
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
	r.mu.Lock()
	if r.active >= 10 {
		r.mu.Unlock()
		return nil, errors.New("MaxSessions exceeded")
	}
	r.active++
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	kind := ""
	switch {
	case strings.Contains(command, "'dyna-hostexec'"):
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

func TestHostexecLanesReserveHeartbeatAndStop(t *testing.T) {
	r := &maxSessionsRunner{entered: make(chan string, 16), release: make(chan struct{}), stopRelease: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 8)
	for range 3 {
		go func() {
			_, _, _, _ = h.RunHostexec(ctx, contract.HostexecRunArgs{Argv: []string{"true"}}, "op")
			done <- struct{}{}
		}()
	}
	for range 3 {
		if kind := <-r.entered; kind != "exec" {
			t.Fatalf("got %s", kind)
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, _, _, err := h.RunHostexec(ctx, contract.HostexecRunArgs{Argv: []string{"true"}}, "extra"); !errors.Is(err, hostexec.ErrHostBusy) {
		t.Fatalf("fourth hostexec: %v", err)
	}
	for range 5 {
		go func() { _, _ = h.run(ctx, "short"); done <- struct{}{} }()
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
	for range 8 {
		<-done
	}
	cancel()
	<-heartbeatDone
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
