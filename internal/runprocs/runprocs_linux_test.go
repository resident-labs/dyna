//go:build linux

package runprocs

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// D-185: a canceled run's workers can sit in process groups of their own. Kill
// ends the groups that carry the run's stamp and leaves other processes alone.
func TestKillEndsStampedGroupsOnly(t *testing.T) {
	start := func(env ...string) *exec.Cmd {
		cmd := exec.Command("sleep", "600")
		cmd.Env = append(os.Environ(), env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	const run = "wf_20261026-010101-abcdef01"
	stamped := start("DYNA_WORKER_RUN=" + run)
	other := start("DYNA_WORKER_RUN=wf_20261026-010101-00000000")
	plain := start()
	if err := Kill(run, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := stamped.Wait(); err == nil || stamped.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("stamped worker did not die by SIGKILL: %v", err)
	}
	for _, c := range []*exec.Cmd{other, plain} {
		if err := syscall.Kill(c.Process.Pid, 0); err != nil {
			t.Fatalf("unrelated process %d was killed: %v", c.Process.Pid, err)
		}
	}
}
