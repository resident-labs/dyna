package shells

import (
	"bufio"
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

// startLostJob records a job whose supervisor is gone and starts script as its
// command: a group leader stamped with the job. It returns the job ID and the
// lines the script prints first. The test reaps the command at its end, so
// the command stays a zombie, holding its group, until then.
func startLostJob(t *testing.T, s *Store, script string, lines int) (string, *bufio.Scanner) {
	t.Helper()
	meta, err := s.Create(StartOptions{Cmd: []string{"sh"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	meta.Started = time.Now().Add(-time.Minute)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close() })
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), JobEnv+"="+meta.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	meta.Pid = cmd.Process.Pid
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewScanner(pr)
	for i := 0; i < lines; i++ {
		if !r.Scan() {
			t.Fatalf("helper printed %d of %d lines", i, lines)
		}
	}
	return meta.ID, r
}

// D-190: a job whose supervisor died but whose command runs on reads as lost.
// Stop must still end the command and the children it started.
func TestStopEndsProcessesOfLostJob(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	id, _ := startLostJob(t, s, "sleep 600 & echo started; wait", 1)
	if in, err := s.Info(id); err != nil || in.Status != "lost" {
		t.Fatalf("job before stop = %q, %v; want lost", in.Status, err)
	}
	in, err := s.Stop(id, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if left := jobProcesses(id, map[int]pinned{}); len(left) != 0 {
		t.Fatalf("processes of the stopped job remain: %v", left)
	}
	if in.Status != "exited" || in.Exit == nil {
		t.Fatalf("stopped job = %q, exit %v; want exited with an exit", in.Status, in.Exit)
	}
	if again, err := s.Info(id); err != nil || again.Status != "exited" {
		t.Fatalf("job after stop = %q, %v; want exited", again.Status, err)
	}
}

// A child in the leader's group may drop the job stamp and ignore SIGTERM. The
// stop still ends it, even though the stamped leader is long gone by then.
func TestStopKillsUnstampedGroupMemberThatIgnoresSigterm(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	// The child prints its PID, then ignores SIGTERM; the script's first line comes from the leader.
	id, out := startLostJob(t, s, `env -i /bin/sh -c 'trap "" TERM; echo $$; exec sleep 600' & wait`, 0)
	requireEnded(t, out, func() { mustStop(t, id) })
}

// The anchor of a group may end and be reaped after the group is read. Its
// number can then name another group, so the candidates read afterwards must
// not be admitted.
func TestReleasedAnchorDoesNotAdmitUnrelatedGroup(t *testing.T) {
	bystander := exec.Command("sleep", "600")
	bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })
	anchor := exec.Command("sleep", "600")
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Process.Kill(); _ = anchor.Wait() })
	fd, err := unix.PidfdOpen(anchor.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// The anchor reads as a member of the bystander's group; when the bystander
	// is read as a candidate, the anchor ends and is reaped.
	realRead, released := readGroup, false
	readGroup = func(pid int) (int, bool) {
		switch pid {
		case anchor.Process.Pid:
			return bystander.Process.Pid, true
		case bystander.Process.Pid:
			if !released {
				released = true
				_ = anchor.Process.Kill()
				_ = anchor.Wait()
			}
		}
		return realRead(pid)
	}
	t.Cleanup(func() { readGroup = realRead })
	set := jobProcesses("sh_20261026-000000-00000000", map[int]pinned{anchor.Process.Pid: {pid: anchor.Process.Pid, fd: fd}})
	if _, admitted := set[bystander.Process.Pid]; admitted {
		t.Fatal("an unrelated process was admitted after its group's anchor was released")
	}
}

// mustStop runs the orphan stop for job id and fails the test on error.
func mustStop(t *testing.T, id string) {
	t.Helper()
	if _, err := stopOrphans(id); err != nil {
		t.Fatal(err)
	}
}

// requireEnded runs stop, then reads the helper's next line as a PID and checks
// that the process has ended.
func requireEnded(t *testing.T, out *bufio.Scanner, stop func()) {
	t.Helper()
	stop()
	if !out.Scan() {
		t.Fatal("helper printed no PID")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.Text()))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return // already gone and reaped
	}
	defer unix.Close(fd)
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0); err != nil || n == 0 {
		t.Fatalf("process %d still runs: %v", pid, err)
	}
}
