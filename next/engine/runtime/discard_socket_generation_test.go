package runtime

import (
	"context"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
)

type discardReplyRunner struct {
	held                                     atomic.Bool
	entered, release, creating, finishCreate chan struct{}
	binary                                   atomic.Bool
}

func (r *discardReplyRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	out, err := (&nativePinnedRunner{}).Run(ctx, in, args...)
	if strings.Contains(command, "'dyna-remove-box'") && r.held.CompareAndSwap(false, true) {
		close(r.entered)
		<-r.release
	}
	if strings.Contains(command, ".activation.lock") && r.binary.CompareAndSwap(false, true) {
		close(r.creating)
		select {
		case <-r.finishCreate:
		case <-ctx.Done():
		}
		// A confirmed install failure ends the new worker only AFTER the listener
		// assertion. No container creation or invented second AddBox is needed.
		return []byte("DYNA-END 00000000000000000000 071\n"), nil
	}
	return out, err
}

// At the runtime discard boundary, drive owner restart and real reconciler
// AddBox while a successful reply is held. This is not a full HostRemove probe:
// its draining state would fail RemoveFailedBox's ready-host precondition.
func TestReadyHostRuntimeDiscardSocket(t *testing.T) {
	f := newRestartFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE boxes SET want='run' WHERE id='b'`)
	f.runtime.Runtime = t.TempDir()
	f.runtime.Version = "test"
	sockets := api.NewSockets(api.New(socketOnlyService{}, nil), f.runtime.Runtime, uint32(os.Getuid()), nil)
	defer sockets.Close()
	f.runtime.Sockets = sockets
	key := stagePinnedFixture(t, f.runtime.Layout.HostBin(), []byte("#!/bin/bash\nexit 0\n"))
	f.runtime.binOnce.Do(func() { f.runtime.binSum = key })
	runner := &discardReplyRunner{entered: make(chan struct{}), release: make(chan struct{}), creating: make(chan struct{}), finishCreate: make(chan struct{})}
	h, err := podman.New("user@host", t.TempDir(), "old", runner, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts["old"] = h
	fresh, err := podman.New("user@new-host", t.TempDir(), "new", runner, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts["new"] = fresh
	old, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); result <- f.runtime.DiscardHostBox(ctx, old) }()
	defer func() {
		select {
		case <-runner.release:
		default:
			close(runner.release)
		}
		select {
		case <-runner.finishCreate:
		default:
			close(runner.finishCreate)
		}
		<-finished
		f.svc.Reconciler().Wait()
	}()
	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discard did not finish its host deletion")
	}
	// Keep the cleanup host ready but quarantine its placement. Explicit
	// restart cleans it before detaching, then really selects another host.
	f.exec(t, `UPDATE hosts SET create_problem='create_failed' WHERE name='old'`)
	restart := f.restartAsync(t, ctx)
	select {
	case problem := <-restart:
		if problem != nil {
			t.Fatal(problem)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner restart could not overtake the held discard reply")
	}
	if err := f.svc.Reconciler().Pass(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.creating:
	case <-time.After(5 * time.Second):
		current, _ := f.st.Box(ctx, "b")
		t.Fatalf("new lifecycle did not open its listener: %+v", current)
	}
	current, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxCreating || current.Host != "new" {
		t.Fatalf("restart did not reach actual creation: %+v", current)
	}
	path := f.runtime.forward("b").EnginePath
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	close(runner.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("old discard reply closed the restarted session's listener: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("discard replaced the restarted session's socket")
	}
	connection, err = net.Dial("unix", path)
	if err != nil {
		t.Fatalf("restarted session socket does not accept connections: %v", err)
	}
	connection.Close()
}
