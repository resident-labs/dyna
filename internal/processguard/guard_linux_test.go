//go:build linux

package processguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func dumpableForTest() (int, error) { return unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0) }

func TestLiveReaperPreservesExitedAnchor(t *testing.T) {
	anchor := exec.Command("/bin/sleep", "60")
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	defer anchor.Wait()
	if err := anchor.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	pid := anchor.Process.Pid
	// A pidfd becomes readable when the anchor exits, which is the event this
	// test needs before it can check the zombie it is not allowed to reap.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 8000); err != nil || n == 0 {
		t.Fatalf("exited anchor: %d %v", n, err)
	}
	reapOrphans(pid)
	fields, err := procFields(pid)
	if err != nil || fields[0] != "Z" {
		t.Fatalf("live reaper consumed the anchor before cleanup: fields=%v error=%v", fields, err)
	}
	// Cleanup owns the anchor's wait and must still receive its exit status.
	err = anchor.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("anchor wait: %v, want SIGKILL status", err)
	}
}

func TestAdoptedChildFixture(t *testing.T) {
	switch os.Getenv("PG_ADOPTED_FIXTURE") {
	case "spawn":
		var pids strings.Builder
		for range 32 {
			cmd := exec.Command("/bin/true")
			if err := cmd.Start(); err != nil {
				os.Exit(101)
			}
			fmt.Fprintln(&pids, cmd.Process.Pid)
		}
		path := filepath.Join(os.Getenv("PG_ROOT"), "orphans")
		if err := os.WriteFile(path+".pending", []byte(pids.String()), 0600); err != nil {
			os.Exit(102)
		}
		if err := os.Rename(path+".pending", path); err != nil {
			os.Exit(102)
		}
		// Exit without Wait so the guard must adopt and reap these children.
		os.Exit(0)
	case "host", "host-exit":
		cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptedChildFixture$")
		cmd.Env = append(os.Environ(), "PG_ADOPTED_FIXTURE=spawn")
		if err := cmd.Run(); err != nil {
			os.Exit(103)
		}
		if os.Getenv("PG_ADOPTED_FIXTURE") == "host" {
			// The parent waits for this line instead of the orphan file.
			_, _ = os.Stdout.WriteString("ready\n")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
		}
		os.Exit(7)
	}
}

func TestGuardPreservesNativeExitWhileReaping(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptedChildFixture$")
	cmd.Env = append(os.Environ(), "PG_ADOPTED_FIXTURE=host-exit", "PG_ROOT="+t.TempDir())
	c := wrapTest(t, cmd)
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("native exit status during orphan exits: %v, want 7", err)
	}
}

func TestGuardReapsAdoptedChildrenWhileRunning(t *testing.T) {
	root := t.TempDir()
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer release.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptedChildFixture$")
	cmd.Env = append(os.Environ(), "PG_ADOPTED_FIXTURE=host", "PG_ROOT="+root)
	cmd.Stdin = input
	readyIn, readyOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyIn.Close()
	cmd.Stdout = readyOut
	c := wrapTest(t, cmd)
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	readyOut.Close()
	defer func() {
		release.Close()
		_ = waitTest(t, c)
	}()
	awaitLine(t, readyIn, "adopted child PIDs")
	b, err := os.ReadFile(filepath.Join(root, "orphans"))
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(b))
	if len(pids) != 32 {
		t.Fatalf("child PIDs: %q", b)
	}
	// The guard reaps adopted children in its SIGCHLD path and reports the
	// running total, so waiting for it to reach 32 is a real reaping event. The
	// total is cumulative, so a guard that reaps in many passes cannot outrun
	// this check.
	for c.Reaped() < len(pids) {
		select {
		case <-c.ReapedChanged():
		case <-time.After(8 * time.Second):
			t.Fatalf("the guard reaped %d of %d adopted children while the native ran", c.Reaped(), len(pids))
		}
	}
	// The pids are gone while the native still runs, so the live reaper did the
	// work and not the final reap during shutdown.
	for _, value := range pids {
		pid, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		if fields, err := procFields(pid); err == nil {
			t.Fatalf("child %d was not reaped while the native ran: state=%s parent=%s", pid, fields[0], fields[1])
		}
	}
	if live, err := Probe(c.Reference()); !live || err != nil {
		t.Fatalf("guard exited before child check: %v %v", live, err)
	}
	release.Close()
	err = waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("native exit status: %v, want 7", err)
	}
	// The guard reaps adopted children in its SIGCHLD path and once more before
	// it exits, so no adopted child may remain afterwards.
	for _, value := range pids {
		pid, _ := strconv.Atoi(value)
		if fields, err := procFields(pid); err == nil {
			t.Fatalf("child %d was not reaped: state=%s parent=%s", pid, fields[0], fields[1])
		}
	}
}

// awaitLeaderExit waits for the group leader's own exit event. A pidfd becomes
// readable on exit, which is the cancellation signal this test needs.
func awaitLeaderExit(t *testing.T, ref Reference) {
	t.Helper()
	leader, _ := ref.Leader()
	fd, err := unix.PidfdOpen(leader, 0)
	if errors.Is(err, unix.ESRCH) {
		return // already gone
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 8000); err != nil || n == 0 {
		t.Fatalf("group leader %d did not exit: %d %v", leader, n, err)
	}
}

func assertNoCrashEvents(t *testing.T, started time.Time, pids ...int) {
	t.Helper()
	journal, err := exec.LookPath("journalctl")
	if err != nil {
		t.Log("journal unavailable; kernel core-dump status is checked")
		return
	}
	args := []string{"--no-pager", "--quiet", "--output=json", "--since=@" + strconv.FormatInt(started.Unix(), 10)}
	for _, pid := range pids {
		args = append(args, "COREDUMP_PID="+strconv.Itoa(pid))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, journal, args...).Output()
	if err != nil {
		t.Logf("journal unavailable: %v; kernel core-dump status is checked", err)
		return
	}
	if len(bytes.TrimSpace(b)) != 0 {
		t.Fatalf("unexpected core-dump journal event: %s", b)
	}
}

func procFieldsForTest(pid int) (string, error) {
	f, e := procFields(pid)
	if e != nil {
		return "", e
	}
	return f[0], nil
}
