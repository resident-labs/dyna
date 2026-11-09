package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

// Gate the actual production command before its flock; do not replace its body.
type epochNamespaceRunner struct{ old bool }

func (r *epochNamespaceRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	if r.old && strings.Contains(command, "dyna-cancel") {
		return nil, errors.New("old transport cannot confirm cancellation")
	}
	if strings.Contains(command, "dyna-box-namespace") {
		who := "new-other"
		if strings.Contains(command, "dyna-create-box") {
			who = "new-create"
		}
		oldDelete := r.old && strings.Contains(command, "dyna-remove-box")
		if oldDelete {
			who = "old"
		}
		gate := ""
		if oldDelete {
			gate = "touch \"$PROPOSED_LOCK_ENTERED\"\nwhile ! test -f \"$PROPOSED_LOCK_RELEASE\"; do sleep 0.01; done\ntrap \"touch \\\"$PROPOSED_LOCK_FINISHED\\\"\" EXIT\n"
		}
		command = strings.Replace(command, "flock -x -w 15 8", gate+"flock -x -w 15 8", 1)
		command = strings.Replace(command, "epoch_file=", "printf \""+who+"-acquired\\n\" >> \"$PROPOSED_LOCK_EVENTS\"\nepoch_file=", 1)
		args = append([]string(nil), args...)
		args[len(args)-1] = command
	}
	return (&nativePinnedRunner{}).Run(ctx, input, args...)
}

func TestHostEpochOrdersStalePreLockDeletionAcrossStoreReopen(t *testing.T) {
	f := unusedStopFixture(t)
	// The native shim must reject wrong managed namespace literals rather than
	// accidentally make an invalid candidate look like compose-volume deletion.
	podmanPath, err := exec.LookPath("podman")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(podmanPath)
	if err != nil {
		t.Fatal(err)
	}
	checks := `set -eu
case " $* " in
 *' ps --all '* ) printf '[]'; exit 0;;
 *' pod rm '*) case " $* " in *' dyna-pod-b '*) ;; *) exit 94 ;; esac ;;
 *' volume rm '*) case " $* " in *' dyna-box-b '*) ;; *) exit 95 ;; esac ;;
 *' volume prune '*) case " $* " in *' --filter label=dyna.stack=b '*) ;; *) exit 96 ;; esac ;;
esac
`
	if !strings.Contains(string(fixture), "set -eu\n") {
		t.Fatal("missing native Podman fixture anchor")
	}
	if err := os.WriteFile(podmanPath, []byte(strings.Replace(string(fixture), "set -eu\n", checks, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE hosts SET engine_host=1 WHERE name='old'`)
	entered := filepath.Join(f.root, "pre-lock-entered")
	release := filepath.Join(f.root, "pre-lock-release")
	lockDir := filepath.Join(f.root, "locks")
	t.Setenv("PROPOSED_LOCK_DIR", lockDir)
	t.Setenv("PROPOSED_LOCK_FILE", filepath.Join(lockDir, "box-b.lock"))
	t.Setenv("PROPOSED_LOCK_ENTERED", entered)
	t.Setenv("PROPOSED_LOCK_RELEASE", release)
	t.Setenv("PROPOSED_LOCK_FINISHED", filepath.Join(f.root, "volume-finished"))
	t.Setenv("PROPOSED_LOCK_EVENTS", filepath.Join(f.root, "lock-events"))
	runner := &epochNamespaceRunner{old: true}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	h, err := podman.New("user@host", t.TempDir(), "old", runner, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.hosts["old"] = h
	engineCtx, stopEngine := context.WithCancel(context.Background())
	defer stopEngine()
	f.runtime.Context = engineCtx
	f.runtime.retryDelay = 5 * time.Millisecond
	work, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- f.runtime.Remove(podman.WithRemoteCancellationDeadline(work, time.Now().Add(5*time.Second)), "b")
	}()
	waitPinnedFile(t, entered)
	epochProbeWake(t, f, "independent-shutdown-wake")
	cancel()
	select {
	case err := <-result:
		var uncertain podman.RemoteCancellationUnconfirmed
		if !errors.As(err, &uncertain) {
			t.Fatalf("lost typed uncertainty: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bounded old caller did not return")
	}
	// Shutdown cancels the engine keeper but must not report a physical join.
	stopEngine()
	joinCtx, stopJoin := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = f.st.JoinBoxVolumeRemovals(joinCtx, "b")
	stopJoin()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown released unconfirmed ownership before store close: %v", err)
	}
	var seq int
	var name, dbPath string
	if err := f.st.Read().QueryRow("PRAGMA database_list").Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// Use a genuinely new process-local Host/Boxes/Service, as runtime.Run does.
	// Hello/Dispatch do not need a host command and therefore never join the old
	// process-local Host.pendingCancels map.
	freshHost, err := podman.New("user@host", t.TempDir(), "old", &epochNamespaceRunner{}, f.root)
	if err != nil {
		t.Fatal(err)
	}
	freshBoxes := &Boxes{Store: reopened, Sockets: f.runtime.Sockets, Layout: box.Layout{Data: f.root, HostData: f.root}, Runtime: t.TempDir(), hosts: map[contract.HostName]*podman.Host{"old": freshHost}, restored: map[contract.HostName]bool{"old": true}, installed: map[contract.HostName]bool{"old": true}}
	key := stagePinnedFixture(t, freshBoxes.Layout.HostBin(), []byte("#!/bin/bash\nexit 0\n"))
	freshBoxes.binOnce.Do(func() { freshBoxes.binSum = key })
	freshService := engine.New(reopened, freshBoxes, stackProfiles{}, func() time.Time { return f.now })
	if err := freshService.Reconciler().FailStaleCreates(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Recreate is the actual startup step for a starting box whose container was
	// removed by the interrupted Stop. Run its real native container create/start,
	// not a Hello from a dead container. Only Podman/systemctl executables are fixtures.
	current, err := reopened.Box(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	slice, err := box.SliceName("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := freshBoxes.Recreate(context.Background(), lifecycle.BoxSpec{Box: "b", Session: "s", Room: "r", Host: "old", Slice: slice, Size: current.Size, Stack: &box.Stack{Volumes: []box.Volume{{Name: "data", Target: "/data"}}}}, "fresh-image"); err != nil {
		t.Fatalf("actual startup Recreate: %v", err)
	}
	epochProbeHelloTurn(t, freshService, reopened, f.now, "independent-after-reopen-turn")
	epochProbeUsedFiles(t, f)
	events, err := os.ReadFile(filepath.Join(f.root, "lock-events"))
	if err != nil || !strings.Contains(string(events), "new-create-acquired\n") || strings.Contains(string(events), "old-acquired") {
		t.Fatalf("did not select new-create-before-old-acquisition schedule: %s %v", events, err)
	}
	commands, err := os.ReadFile(f.log)
	if err != nil || !strings.Contains(string(commands), " create --name b ") {
		t.Fatalf("actual main container CREATE did not run: %s %v", commands, err)
	}
	t.Logf("lock trace before releasing old command:\n%s", events)
	// The old physical deletion now resumes after the first real turn of the new
	// engine. Marker is written by the same actual framed remote shell.
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitPinnedFile(t, filepath.Join(f.root, "volume-finished"))
	after, err := os.ReadFile(filepath.Join(f.root, "lock-events"))
	if err != nil || !strings.Contains(string(after), "old-acquired\n") {
		t.Fatalf("old atomic deletion did not acquire the namespace lock: %s %v", after, err)
	}
	t.Logf("lock trace after old deletion:\n%s", after)
	for _, dir := range []string{f.workspace, f.stack} {
		assertRestartFile(t, dir, "independent-used-work", true)
	}
}

func epochProbeWake(t *testing.T, f *restartFixture, label string) {
	t.Helper()
	if err := f.st.WakeWithNotice(context.Background(), contract.Event{ID: contract.EventID(label), Room: "r", Time: f.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionNotice{Session: "s", Text: label}}, "s"); err != nil {
		t.Fatal(err)
	}
}
func epochProbeHelloTurn(t *testing.T, svc *engine.Service, st *store.Store, at time.Time, label string) {
	t.Helper()
	if _, problem := svc.BoxHello(context.Background(), contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatalf("actual Hello: %v", problem)
	}
	active, inputs, _, err := st.Dispatch(context.Background(), "s", contract.TurnID(label), contract.Event{ID: contract.EventID(label), Room: "r", Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}}, false)
	if err != nil || active != contract.TurnID(label) || len(inputs) == 0 {
		t.Fatalf("actual Dispatch: active=%s inputs=%v err=%v", active, inputs, err)
	}
}
func epochProbeUsedFiles(t *testing.T, f *restartFixture) {
	t.Helper()
	for _, dir := range []string{f.workspace, f.stack} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "independent-used-work"), []byte("written only after actual Hello and Dispatch"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
