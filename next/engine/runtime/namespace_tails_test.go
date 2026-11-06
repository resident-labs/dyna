package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type namespaceTailGate struct {
	after            bool
	match            []string
	once             sync.Once
	entered, release chan struct{}
}

func (r *namespaceTailGate) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	hit := false
	for _, match := range r.match {
		hit = hit || strings.Contains(args[len(args)-1], match)
	}
	gate := func() { r.once.Do(func() { close(r.entered); <-r.release }) }
	if hit && !r.after {
		gate()
	}
	out, err := (&nativePinnedRunner{}).Run(ctx, in, args...)
	if hit && r.after {
		gate()
	}
	return out, err
}

func TestSupersededCreateCleanupHasNoSliceTail(t *testing.T) {
	f := newRestartFixture(t)
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(systemctl, []byte("#!/bin/bash\nprintf 'systemctl %s\\n' \"$*\" >> \"$RESTART_LOG\"\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	gate := &namespaceTailGate{match: []string{"'dyna-remove-box'"}, entered: make(chan struct{}), release: make(chan struct{})}
	old, err := podman.New("user@host", t.TempDir(), "old", gate, f.root)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := podman.New("user@host", t.TempDir(), "fresh", &nativePinnedRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	slice, err := box.SliceName("b")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		cleanupCreate(context.Background(), old, lifecycle.BoxSpec{Box: "b", Slice: slice}, box.Forward{}, false, true, false, true)
		close(done)
	}()
	<-gate.entered
	if err := fresh.EnsureSlice(context.Background(), box.Slice{Name: slice}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new", Slice: slice}, nil, false); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	<-done
	calls, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	create := strings.Index(string(calls), "create --name b ")
	if create < 0 {
		t.Fatal("no replacement create", string(calls))
	}
	if strings.Contains(string(calls)[create:], "systemctl --user stop "+slice) {
		t.Fatalf("superseded cleanup stopped replacement slice:\n%s", calls)
	}
}

func TestStopDoesNotRefreshEpochForPodTail(t *testing.T) {
	f := newRestartFixture(t)
	gate := &namespaceTailGate{after: true, match: []string{"'dyna-stop-box'", "'stop' '--ignore' 'b'"}, entered: make(chan struct{}), release: make(chan struct{})}
	old, err := podman.New("user@host", t.TempDir(), "old", gate, f.root)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := podman.New("user@host", t.TempDir(), "fresh", &nativePinnedRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts["old"] = old
	done := make(chan error, 1)
	go func() { done <- f.runtime.Stop(context.Background(), "b") }()
	<-gate.entered
	if err := fresh.StartContainer(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(calls), "start b\n")
	if start < 0 {
		t.Fatal("no replacement warm start", string(calls))
	}
	if strings.Contains(string(calls)[start:], "pod stop --ignore dyna-pod-b") {
		t.Fatalf("old Stop stopped replacement pod:\n%s", calls)
	}
}

func TestSupersededDiscardIsTerminalAndKeepsNewWork(t *testing.T) {
	f := newRestartFixture(t)
	gate := &namespaceTailGate{match: []string{"'dyna-remove-box'"}, entered: make(chan struct{}), release: make(chan struct{})}
	old, err := podman.New("user@host", t.TempDir(), "old", gate, f.root)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := podman.New("user@host", t.TempDir(), "fresh", &nativePinnedRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts["old"] = old
	record, err := f.st.Box(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	before, err := fresh.BoxEpoch(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.runtime.DiscardHostBox(context.Background(), record) }()
	<-gate.entered
	if err := fresh.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, nil, true); err != nil {
		t.Fatal(err)
	}
	now, err := fresh.BoxEpoch(context.Background(), "b")
	if err != nil || now <= before {
		t.Fatalf("replacement did not advance epoch: %d %d %v", before, now, err)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		if err := os.WriteFile(filepath.Join(dir, "new-work"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatalf("superseded discard was returned as failure: %v", err)
	}
	after, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != string(after) {
		t.Fatalf("superseded discard mutated new namespace:\n%s", after)
	}
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "new-work", true)
	}
}
