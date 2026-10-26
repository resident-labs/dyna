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
// lines the script prints first.
func startLostJob(t *testing.T, s *Store, script string, lines int) (string, []string) {
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
	defer pr.Close()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), JobEnv+"="+meta.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	meta.Pid = cmd.Process.Pid
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	var got []string
	r := bufio.NewScanner(pr)
	for len(got) < lines && r.Scan() {
		got = append(got, r.Text())
	}
	if len(got) != lines {
		t.Fatalf("helper printed %v, want %d lines", got, lines)
	}
	return meta.ID, got
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
	id, out := startLostJob(t, s, `env -i /bin/sh -c 'trap "" TERM; echo $$; exec sleep 600' & wait`, 1)
	child, err := strconv.Atoi(strings.TrimSpace(out[0]))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.PidfdOpen(child, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// No grace: the leader ends on SIGTERM and the child must get SIGKILL.
	if _, err := stopOrphans(id, 0); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0); err != nil || n == 0 {
		t.Fatalf("the child that ignores SIGTERM still runs: %v", err)
	}
}
