package podman

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestShortCommandsLeaveHeartbeatAndHostexecUnblocked(t *testing.T) {
	r := &limitRunner{entered: make(chan struct{}, 9), release: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 8)
	for range 8 {
		go func() { _, _ = h.run(ctx, "short"); done <- struct{}{} }()
	}
	for range 8 {
		<-r.entered
	}
	// A ninth short command must wait for a slot, and cancellation must stop it.
	ninth, stop := context.WithCancel(ctx)
	stop()
	if _, err := h.run(ninth, "short"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ninth command: %v", err)
	}
	select {
	case <-r.entered:
		t.Fatal("ninth command reached SSH")
	default:
	}
	ticks := make(chan time.Time, 1)
	reports := make(chan bool, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		h.Heartbeat(ctx, ticks, "/data", func(ok bool, _ Load) { reports <- ok }, func(string, contract.PressureKind) {})
	}()
	ticks <- time.Time{}
	if !<-reports {
		t.Fatal("heartbeat failed while short commands were busy")
	}
	if err := h.StopHostexec(ctx, "scope"); err != nil {
		t.Fatalf("hostexec stop: %v", err)
	}
	close(r.release)
	for range 8 {
		<-done
	}
	cancel()
	<-heartbeatDone
}
