//go:build linux

package runprocs

import (
	"bufio"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// startSleeper starts a process that waits with env. It returns once the shell
// has printed its ready line. The shell then blocks on stdin instead of
// exec'ing another program: an exec would empty /proc/<pid>/environ again, and
// Kill finds a process through that environment.
func startSleeper(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "echo ready; read _")
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// D-185: a canceled run's workers can sit in process groups of their own. Kill
// ends the groups that carry the run's stamp and leaves other processes alone.
func TestKillEndsStampedGroupsOnly(t *testing.T) {
	const run = "wf_20261026-010101-abcdef01"
	stamped := startSleeper(t, "DYNA_WORKER_RUN="+run)
	other := startSleeper(t, "DYNA_WORKER_RUN=wf_20261026-010101-00000000")
	plain := startSleeper(t)
	if err := Kill(run, 0, nil, nil); err != nil {
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

// A process of a kept job is spared although it carries the run's stamp; the
// same pass still ends a stamped process of another group.
func TestKillSparesProcessesOfKeptJobs(t *testing.T) {
	const run = "wf_20261026-010101-abcdef01"
	worker := startSleeper(t, "DYNA_WORKER_RUN="+run)
	kept := startSleeper(t, "DYNA_WORKER_RUN="+run, "DYNA_SHELL_JOB=sh_kept")
	if err := Kill(run, 0, nil, []string{"DYNA_SHELL_JOB=sh_kept"}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err == nil {
		t.Fatal("worker survived")
	}
	// Signal 0 succeeds for an unreaped zombie, so read the state instead.
	if st, ok := readStat(kept.Process.Pid); !ok || st.state == 'Z' {
		t.Fatalf("kept job process was killed: %+v, found=%v", st, ok)
	}
}
