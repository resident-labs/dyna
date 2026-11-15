package podman

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNamespaceRecoveryRefusesZombieLeaderWithLiveThread(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler unavailable")
	}
	root := t.TempDir()
	source := filepath.Join(root, "holder.c")
	// pthread_exit terminates only the group leader. The worker retains the
	// shared descriptor table and flock even though /proc/PID/stat says Z.
	const fixture = `#include <fcntl.h>
#include <pthread.h>
#include <sys/file.h>
#include <unistd.h>
static void *worker(void *unused) {
    (void)unused;
    for (;;) pause();
    return 0;
}
int main(int argc, char **argv) {
    if (argc != 2) return 1;
    int fd = open(argv[1], O_CREAT | O_RDWR, 0600);
    if (fd < 0 || flock(fd, LOCK_EX)) return 2;
    pthread_t thread;
    if (pthread_create(&thread, 0, worker, 0)) return 3;
    pthread_exit(0);
}
`
	if err := os.WriteFile(source, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "live-worker")
	if out, err := exec.Command(cc, "-pthread", source, "-o", executable).CombinedOutput(); err != nil {
		t.Fatalf("compile holder: %v: %s", err, out)
	}
	lock := filepath.Join(root, "locks", "box-b.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, lock)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := strconv.Itoa(child.Process.Pid)
	proc := "/proc/" + pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		stat, err := os.ReadFile(filepath.Join(proc, "stat"))
		if err == nil {
			_, tail, found := strings.Cut(string(stat), ") ")
			if found && strings.HasPrefix(tail, "Z ") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("holder leader did not become a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	tasks, err := os.ReadDir(filepath.Join(proc, "task"))
	if err != nil || len(tasks) < 2 {
		t.Fatalf("expected zombie leader and live worker: %v: %v", tasks, err)
	}
	if out, err := exec.Command("flock", "-n", lock, "true").CombinedOutput(); err == nil {
		t.Fatalf("live worker did not retain flock: %s", out)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("competing flock failed unexpectedly: %v: %s", err, out)
		}
	}
	// Exercise the classification directly as well as through the scan, so a
	// permissive host's procfs access cannot hide an incorrect Z exemption.
	if err := exec.Command("bash", "-c", namespaceProcAccess+`namespace_process_gone "$1"`, "fixture", proc).Run(); err == nil {
		t.Error("zombie leader with a live thread classified as gone")
	}
	fixtureProc := filepath.Join(root, "fixture-proc")
	if err := os.MkdirAll(fixtureProc, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(proc, filepath.Join(fixtureProc, pid)); err != nil {
		t.Fatal(err)
	}
	out, err := namespaceCommand(root, "recover", "--check").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 254 {
		t.Fatalf("check must refuse hidden live holder, got %v: %s", err, out)
	}
}
