package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

func namespaceFixture(t *testing.T) (string, *Host) {
	t.Helper()
	root := t.TempDir()
	tools := t.TempDir()
	script := `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$NS_ROOT/calls"
if [ "${1:-}" = --cgroup-manager=systemd ]; then shift; fi
case "$1 $2" in
 'volume create') mkdir -p "$NS_ROOT/stack";;
 'volume rm')
  [ "$3" = dyna-box-b ] || exit 95
  test -d "$NS_ROOT/workspace" || exit 1
  if test -f "$NS_ROOT/gate-child"; then
   printf '%s %s\n' "$PPID" "$(ps -o ppid= -p "$PPID")" > "$NS_CHILD_READY"
   IFS= read -r _ < "$NS_CHILD_RELEASE"
  fi
  rm -rf "$NS_ROOT/workspace";;
 'volume prune')
  [[ " $* " == *' --filter label=dyna.stack=b '* ]] || exit 96
  rm -rf "$NS_ROOT/stack";;
 'pod rm') [[ " $* " == *' dyna-pod-b '* ]] || exit 94;;
 *)
  if [ "$1" = create ]; then
   mkdir -p "$NS_ROOT/workspace"
   test -s "$NS_ROOT/locks/box-b.epoch" || exit 97
  fi;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("NS_ROOT", root)
	h, err := New("user@host", t.TempDir(), "h", nativeShellRunner{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return root, h
}

func readNamespaceFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNamespaceMissingWorkspaceStillDeletesStack(t *testing.T) {
	root, h := namespaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "stack"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveVolume(context.Background(), "b"); err != nil {
		t.Fatalf("missing workspace prevented stack cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "stack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stack retained: %v", err)
	}
}

func TestNamespaceEpochSupersedesWholeWorkspaceAndStackDelete(t *testing.T) {
	root, h := namespaceFixture(t)
	ctx := context.Background()
	before, err := h.BoxEpoch(ctx, "b")
	if err != nil || before != 0 {
		t.Fatalf("initial epoch: %d %v", before, err)
	}
	stack := &box.Stack{Volumes: []box.Volume{{Name: "data", Target: "/data"}}}
	c := box.ContainerSpec{Box: "b", Image: "fixture", Volumes: stack.Volumes}
	if err := h.CreateBox(ctx, c, stack, true); err != nil {
		t.Fatal(err)
	}
	now, err := h.BoxEpoch(ctx, "b")
	if err != nil || now != 1 {
		t.Fatalf("create epoch: %d %v", now, err)
	}
	for _, dir := range []string{"workspace", "stack"} {
		if err := os.WriteFile(filepath.Join(root, dir, "used"), []byte("new work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := readNamespaceFile(t, filepath.Join(root, "calls"))
	err = h.RemoveBox(ctx, "b", before, true, "")
	var superseded NamespaceSuperseded
	if !errors.As(err, &superseded) {
		t.Fatalf("stale delete: %v", err)
	}
	after := readNamespaceFile(t, filepath.Join(root, "calls"))
	if string(calls) != string(after) {
		t.Fatalf("superseded deleter touched namespace:\n%s", after)
	}
	for _, dir := range []string{"workspace", "stack"} {
		if _, err := os.Stat(filepath.Join(root, dir, "used")); err != nil {
			t.Fatalf("lost %s work: %v", dir, err)
		}
	}
	if err := h.RemoveBox(ctx, "b", now, true, ""); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"workspace", "stack"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("current delete kept %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "locks", "box-b.lock")); err != nil {
		t.Fatal("deletion removed stable lock:", err)
	}
}

// This gates the ACTUAL production command before its flock, without inserting
// any test lock or replacement body. The cancellation transport cannot join it.
type delayedNamespaceRunner struct{ entered, release, finished *scriptSignal }

func (r delayedNamespaceRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	if strings.Contains(command, "dyna-cancel") {
		return nil, errors.New("old cancellation transport lost")
	}
	if strings.Contains(command, "'delete'") {
		gate := "echo entered > " + quote(r.entered.path) + "; IFS= read -r _ < " + quote(r.release.path) + "\n"
		gate += "trap " + quote("echo finished > "+quote(r.finished.path)) + " EXIT\n"
		if strings.Count(command, `flock -x -w "$remaining" 8`) != 1 {
			return nil, errors.New("namespace flock anchor changed")
		}
		command = strings.Replace(command, `flock -x -w "$remaining" 8`, strings.ReplaceAll(gate, "'", `'\''`)+`flock -x -w "$remaining" 8`, 1)
		args = append([]string(nil), args...)
		args[len(args)-1] = command
	}
	return (nativeShellRunner{}).Run(ctx, in, args...)
}

func TestNamespaceOldPreLockCommandCannotDeleteNewCreation(t *testing.T) {
	root, h := namespaceFixture(t)
	entered, release, finished := newScriptSignal(t), newScriptSignal(t), newScriptSignal(t)
	old, err := New("user@host", t.TempDir(), "old", delayedNamespaceRunner{entered: entered, release: release, finished: finished}, root)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := old.BoxEpoch(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- old.RemoveBox(WithRemoteCancellation(ctx), "b", epoch, true, "") }()
	defer release.write(t, "go")
	if got := entered.read(t); got != "entered" {
		t.Fatalf("old command readiness: %q", got)
	}
	cancel()
	var uncertain RemoteCancellationUnconfirmed
	if err := <-done; !errors.As(err, &uncertain) {
		t.Fatalf("lost uncertainty: %v", err)
	}
	if err := h.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, &box.Stack{Volumes: []box.Volume{{Name: "data"}}}, true); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"workspace", "stack"} {
		if err := os.WriteFile(filepath.Join(root, dir, "used"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := readNamespaceFile(t, filepath.Join(root, "calls"))
	release.write(t, "go")
	if got := finished.read(t); got != "finished" {
		t.Fatalf("old command completion: %q", got)
	}
	after := readNamespaceFile(t, filepath.Join(root, "calls"))
	if string(calls) != string(after) {
		t.Fatalf("old command mutated after new create: %s", after)
	}
	for _, dir := range []string{"workspace", "stack"} {
		if _, err := os.Stat(filepath.Join(root, dir, "used")); err != nil {
			t.Fatalf("old deleter erased %s: %v", dir, err)
		}
	}
}

func TestNamespaceMutatingChildInheritsLockAfterWrapperDeath(t *testing.T) {
	root, h := namespaceFixture(t)
	ctx := context.Background()
	if err := h.CreateBox(ctx, box.ContainerSpec{Box: "b", Image: "old"}, nil, false); err != nil {
		t.Fatal(err)
	}
	epoch, err := h.BoxEpoch(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gate-child"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	entered, release := newScriptSignal(t), newScriptSignal(t)
	t.Setenv("NS_CHILD_READY", entered.path)
	t.Setenv("NS_CHILD_RELEASE", release.path)
	defer release.write(t, "go")
	done := make(chan error, 1)
	go func() { done <- h.RemoveBox(ctx, "b", epoch, true, "") }()
	pids := strings.Fields(entered.read(t))
	if len(pids) != 2 {
		t.Fatalf("namespace wrapper PIDs: %q", pids)
	}
	for _, raw := range pids {
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 0 {
			t.Fatalf("namespace PID %q: %v", raw, err)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	}
	contender, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = h.BoxEpoch(contender, "b")
	stop()
	if err == nil {
		t.Fatal("wrapper death unlocked while the mutating child survived")
	}
	// A different ID remains independent while b is fenced.
	if _, err := h.BoxEpoch(ctx, "different"); err != nil {
		t.Fatal(err)
	}
	release.write(t, "go")
	<-done
	if _, err := h.BoxEpoch(ctx, "b"); err != nil {
		t.Fatalf("lock did not release after child exit: %v", err)
	}
}

func TestNamespaceCreateHasBoundedLockWaitAndNoFallback(t *testing.T) {
	root, h := namespaceFixture(t)
	lockDir := filepath.Join(root, "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(lockDir, "box-b.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = h.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, nil, false)
	var uncertain NamespaceUncertain
	if !errors.As(err, &uncertain) || time.Since(start) > 20*time.Second {
		t.Fatalf("lock wait was not bounded typed uncertainty: %v (%s)", err, time.Since(start))
	}
	if !strings.Contains(err.Error(), "pid="+strconv.Itoa(os.Getpid())) || !strings.Contains(err.Error(), "command=") {
		t.Fatalf("lock failure omitted holder identity: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("diagnostics disturbed holder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "calls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("create escaped contended lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := h.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, nil, false); err != nil {
		t.Fatal(err)
	}
}

// Podman's conmon daemon inherits FD 8 even after podman start returns. The
// completed command must release that shared lock without waiting for conmon.
// namespaceOrphan leaves a real executable holding inherited FD 8 after the
// entire wrapper process group dies. A symlink to sleep would still have an exe
// basename of sleep, so copy it instead.
func namespaceOrphan(t *testing.T, root, name string) int {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(executable, readNamespaceFile(t, sleep), 0700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "orphan")
	cmd := exec.Command("bash", "-c", namespaceScript, "fixture", root, "b", "use", "bash", "-c", `setsid "$1" 120 </dev/null >/dev/null 2>&1 &
printf '%s\n' "$!" > "$2"
wait`, "fixture", executable, pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		if pid > 0 && filepath.Base(exe) == name {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil || filepath.Base(exe) != name {
		t.Fatalf("fixture exe: %q %v", exe, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	return pid
}

func namespaceCommand(root, mode string, args ...string) *exec.Cmd {
	return exec.Command("bash", append([]string{"-c", namespaceScript, "fixture", root, "b", mode}, args...)...)
}

func TestNamespaceUnreadableProcDecision(t *testing.T) {
	uid := os.Geteuid()
	for _, tc := range []struct {
		name   string
		status string
		skip   bool
	}{
		{"other user", fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", uid+1, uid+1, uid+1, uid+1), true},
		{"same real uid", fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", uid, uid+1, uid+1, uid+1), false},
		{"same effective uid", fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", uid+1, uid, uid, uid), false},
		{"no uid", "Name:\tfixture\n", false},
		{"missing status", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := t.TempDir()
			if tc.status != "" {
				if err := os.WriteFile(filepath.Join(proc, "status"), []byte(tc.status), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := exec.Command("bash", "-c", namespaceProcAccess+`namespace_other_user "$1"`, "fixture", proc).Run()
			if (err == nil) != tc.skip {
				t.Fatalf("skip = %v, want %v (%v)", err == nil, tc.skip, err)
			}
		})
	}
}

func TestNamespaceStaleConmonRecovery(t *testing.T) {
	root, h := namespaceFixture(t)
	pid := namespaceOrphan(t, root, "conmon")
	path := filepath.Join(root, "locks", "box-b.lock")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions: %v", before.Mode())
	}
	if _, err := h.BoxEpoch(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("not rotated: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("recovery killed conmon: %v", err)
	}
}

func TestNamespaceBody252DoesNotRecover(t *testing.T) {
	root, h := namespaceFixture(t)
	_, err := h.namespace(context.Background(), "b", "use", "bash", "-c", "exit 252")
	var exit remoteExit
	if !errors.As(err, &exit) || exit != 1 {
		t.Fatalf("body status not distinguished from lock timeout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "locks", "box-b.rotate.new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("body failure invoked recovery")
	}
}

func TestNamespaceLiveHolderNotRecovered(t *testing.T) {
	root, h := namespaceFixture(t)
	namespaceOrphan(t, root, "worker")
	path := filepath.Join(root, "locks", "box-b.lock")
	before, _ := os.Stat(path)
	_, err := h.BoxEpoch(context.Background(), "b")
	var uncertain NamespaceUncertain
	if !errors.As(err, &uncertain) {
		t.Fatalf("live holder not fenced: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("live lock rotated: %v", err)
	}
}

func TestNamespaceConcurrentRecovery(t *testing.T) {
	root := t.TempDir()
	pid := namespaceOrphan(t, root, "conmon")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- namespaceCommand(root, "recover").Run() }()
	}
	// This waiter may open either inode. It must run exactly once, serialized
	// with a fresh acquirer even if it opened the abandoned inode.
	body := `mkdir "$1/critical" || exit 90
sleep 0.2
rmdir "$1/critical"`
	waiter := namespaceCommand(root, "use", "bash", "-c", body, "fixture", root)
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	success := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			success++
		} else {
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 254 {
				t.Fatalf("unexpected recovery failure: %v", err)
			}
		}
	}
	if success != 1 {
		t.Fatalf("rotations: %d", success)
	}
	fresh := namespaceCommand(root, "use", "bash", "-c", body, "fixture", root)
	if err := fresh.Start(); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if err := waiter.Wait(); err != nil {
		t.Fatalf("old acquirer: %v", err)
	}
	if err := fresh.Wait(); err != nil {
		t.Fatalf("fresh acquirer: %v", err)
	}
}

func TestNamespaceConmonExitsBeforeRotation(t *testing.T) {
	root := t.TempDir()
	pid := namespaceOrphan(t, root, "conmon")
	entered, release := newScriptSignal(t), newScriptSignal(t)
	defer release.write(t, "go")
	mv, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	gate := "#!/bin/bash\nprintf 'entered\\n' > " + quote(entered.path) + "; IFS= read -r _ < " + quote(release.path) + "\nexec " + quote(mv) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "mv"), []byte(gate), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	recovery := namespaceCommand(root, "recover")
	if err := recovery.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovery.Process.Kill(); _ = recovery.Wait() })
	if got := entered.read(t); got != "entered" {
		t.Fatalf("mv gate: %q", got)
	}
	body := `mkdir "$1/critical" || exit 90
sleep 0.2
rmdir "$1/critical"`
	waiter := namespaceCommand(root, "use", "bash", "-c", body, "fixture", root)
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waiter.Process.Kill(); _ = waiter.Wait() })
	_ = syscall.Kill(pid, syscall.SIGKILL)
	rotate := filepath.Join(root, "locks", "box-b.rotate")
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(holdersOf(t, rotate), waiter.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(holdersOf(t, rotate), waiter.Process.Pid) {
		t.Fatal("old-inode acquirer did not join rotation fence")
	}
	if _, err := os.Stat(filepath.Join(root, "critical")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old-inode work started before rename")
	}
	release.write(t, "go")
	if err := recovery.Wait(); err != nil {
		t.Fatal(err)
	}
	fresh := namespaceCommand(root, "use", "bash", "-c", body, "fixture", root)
	if err := fresh.Run(); err != nil {
		t.Fatalf("fresh acquirer: %v", err)
	}
	if err := waiter.Wait(); err != nil {
		t.Fatalf("old acquirer: %v", err)
	}
}

func TestNamespaceWaiterRechecksRotatedInode(t *testing.T) {
	root := t.TempDir()
	pid := namespaceOrphan(t, root, "conmon")
	path := filepath.Join(root, "locks", "box-b.lock")
	marker := filepath.Join(root, "ran")
	waiter := namespaceCommand(root, "use", "touch", marker)
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = waiter.Process.Kill(); _ = waiter.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(holdersOf(t, path), waiter.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(holdersOf(t, path), waiter.Process.Pid) {
		t.Fatal("waiter never opened old inode")
	}
	if out, err := namespaceCommand(root, "recover").CombinedOutput(); err != nil {
		t.Fatalf("recover: %v: %s", err, out)
	}
	lock, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	// Once FD 8 points at the new inode, it must remain blocked by our lock.
	deadline = time.Now().Add(5 * time.Second)
	for !slices.Contains(holdersOf(t, path), waiter.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Contains(holdersOf(t, path), waiter.Process.Pid) {
		t.Fatal("waiter did not reopen rotated inode")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("waiter ran without new lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceCompletedCommandReleasesInheritedLock(t *testing.T) {
	for _, status := range []string{"0", "42"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "daemon-pid")
			body := `sleep 60 </dev/null >/dev/null 2>&1 &
printf '%s\n' "$!" > "$1"
exit "$2"`
			cmd := exec.Command("bash", "-c", namespaceScript, "dyna-box-namespace", root, "b", "use", "bash", "-c", body, "daemon-fixture", pidFile, status)
			out, err := cmd.CombinedOutput()
			raw := readNamespaceFile(t, pidFile)
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if status == "0" && err != nil {
				t.Fatalf("namespace command failed: %v: %s", err, out)
			}
			if status != "0" {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 42 {
					t.Fatalf("lost command exit status: %v: %s", err, out)
				}
			}
			fd, err := os.OpenFile(filepath.Join(root, "locks", "box-b.lock"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer fd.Close()
			if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatalf("completed command left namespace locked by its daemon: %v", err)
			}
		})
	}
}

// holdersOf lists the processes that have the file open, from their descriptors.
func holdersOf(t *testing.T, path string) []int {
	t.Helper()
	want, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	links, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, link := range links {
		if got, err := os.Stat(link); err == nil && os.SameFile(want, got) {
			pid, _ := strconv.Atoi(strings.Split(link, "/")[2])
			pids = append(pids, pid)
		}
	}
	return slices.Compact(pids)
}

func TestNamespaceLockHolderScanLeavesNothingBehind(t *testing.T) {
	root, h := namespaceFixture(t)
	lockDir := filepath.Join(root, "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(lockDir, "box-b.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	err = h.CreateBox(context.Background(), box.ContainerSpec{Box: "b", Image: "new"}, nil, false)
	var uncertain NamespaceUncertain
	if !errors.As(err, &uncertain) || !strings.Contains(err.Error(), "pid="+strconv.Itoa(os.Getpid())) {
		t.Fatalf("lock failure lost its type or its holder: %v", err)
	}
	// The wrapper and the scan have returned, so the test alone has the lock file open: the diagnostic is not a holder itself.
	if pids := holdersOf(t, path); !slices.Equal(pids, []int{os.Getpid()}) {
		t.Fatalf("processes with the lock file open after the failure: %v, want only %d", pids, os.Getpid())
	}
}
