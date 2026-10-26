package shells

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// D-190: a job whose supervisor died but whose command runs on reads as lost.
// Stop must still end the command and the children it started.
func TestStopEndsProcessesOfLostJob(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"sh"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	meta.Started = time.Now().Add(-time.Minute)

	// The command leads a group and starts a child in the same group, like ssh
	// under flock. It stays alive because the pipe it reads never closes.
	cmd := exec.Command("sh", "-c", "sleep 600 & wait")
	cmd.Env = append(os.Environ(), JobEnv+"="+meta.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	meta.Pid = cmd.Process.Pid
	if err := s.Save(meta, nil); err != nil {
		t.Fatal(err)
	}
	if len(orphanPids(meta.ID)) == 0 {
		t.Fatal("the helper process is not stamped with the job")
	}
	if in, err := s.Info(meta.ID); err != nil || in.Status != "lost" {
		t.Fatalf("job before stop = %q, %v; want lost", in.Status, err)
	}

	in, err := s.Stop(meta.ID, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if left := orphanPids(meta.ID); len(left) != 0 {
		t.Fatalf("processes of the stopped job remain: %v", left)
	}
	if in.Status != "exited" || in.Exit == nil {
		t.Fatalf("stopped job = %q, exit %v; want exited with an exit", in.Status, in.Exit)
	}
	if again, err := s.Info(meta.ID); err != nil || again.Status != "exited" {
		t.Fatalf("job after stop = %q, %v; want exited", again.Status, err)
	}
}
