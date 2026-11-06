package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/engine/api"
)

// This uses the production framed command and epoch check. The Unix listener is
// the replacement's actual host endpoint, not just a Podman-command recorder.
func TestStaleDeleteSocket(t *testing.T) {
	f := newRestartFixture(t)
	h, err := podman.New("user@host", t.TempDir(), "old", &nativePinnedRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := podman.New("user@host", t.TempDir(), "fresh", &nativePinnedRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := podman.WithRemoteCancellation(context.Background())
	oldEpoch, err := h.BoxEpoch(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.CreateBox(ctx, box.ContainerSpec{Box: "b", Image: "new"}, nil, true); err != nil {
		t.Fatal(err)
	}
	current, err := fresh.BoxEpoch(ctx, "b")
	if err != nil || current <= oldEpoch {
		t.Fatalf("replacement did not advance epoch: %d %d %v", oldEpoch, current, err)
	}
	forward := f.runtime.forward("b")
	if err := os.MkdirAll(filepath.Dir(forward.HostPath), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", forward.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	before, err := os.Lstat(forward.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.st.Box(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	err = removeHostBoxAtEpoch(ctx, h, record, forward, oldEpoch, true)
	var superseded box.NamespaceSuperseded
	if !errors.As(err, &superseded) {
		t.Fatalf("stale delete did not report superseded: %v", err)
	}
	after, err := os.Lstat(forward.HostPath)
	if err != nil {
		t.Fatalf("superseded delete removed replacement socket: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("superseded delete replaced the socket inode")
	}
	connection, err := net.Dial("unix", forward.HostPath)
	if err != nil {
		t.Fatalf("replacement socket no longer accepts connections: %v", err)
	}
	connection.Close()
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", true)
	}
	// Positive control: the current epoch must still remove that very endpoint,
	// along with both authorized named-volume sets, under the same host command.
	if err := removeHostBoxAtEpoch(ctx, h, record, forward, current, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(forward.HostPath); !os.IsNotExist(err) {
		t.Fatalf("current delete left socket: %v", err)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "old-work", false)
	}
}

// Only the listener is exercised; no HTTP method is invoked by these dials.
type socketOnlyService struct{ api.Service }

// Superseded is terminal at the adapter too: it must not fall through into
// closing the engine's listener, whether it loses at Stop, proof, or fallback.
func TestOldRemoveSocket(t *testing.T) {
	for _, phase := range []string{"pre-stop", "retained", "unused"} {
		t.Run(phase, func(t *testing.T) {
			var f *restartFixture
			if phase == "unused" {
				f = unusedStopFixture(t)
			} else {
				f = newRestartFixture(t)
			}
			tag := "'dyna-remove-box'"
			if phase == "pre-stop" {
				tag = "'dyna-stop-box'"
			}
			gate := &namespaceTailGate{match: []string{tag}, entered: make(chan struct{}), release: make(chan struct{})}
			old, err := podman.New("user@host", t.TempDir(), "old", gate, f.root)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := podman.New("user@host", t.TempDir(), "fresh", &nativePinnedRunner{}, f.root)
			if err != nil {
				t.Fatal(err)
			}
			f.runtime.hosts["old"] = old
			f.runtime.Runtime = t.TempDir()
			sockets := api.NewSockets(api.New(socketOnlyService{}, nil), f.runtime.Runtime, uint32(os.Getuid()), nil)
			defer sockets.Close()
			f.runtime.Sockets = sockets
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); result <- f.runtime.Remove(context.Background(), "b") }()
			defer func() {
				select {
				case <-gate.release:
				default:
					close(gate.release)
				}
				<-finished
			}()
			<-gate.entered
			if err := fresh.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, nil, true); err != nil {
				t.Fatal(err)
			}
			if err := f.runtime.Sockets.AddBox("b", "s", "r"); err != nil {
				t.Fatal(err)
			}
			path := f.runtime.forward("b").EnginePath
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			if err := <-result; err != nil {
				t.Fatalf("superseded Remove is not terminal: %v", err)
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("superseded Remove closed the engine socket: %v", err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("engine listener was replaced")
			}
			connection, err := net.Dial("unix", path)
			if err != nil {
				t.Fatalf("engine listener no longer accepts connections: %v", err)
			}
			connection.Close()
		})
	}
}
