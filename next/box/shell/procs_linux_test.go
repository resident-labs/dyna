package shell

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readGroup finds a real process group's processes, their command lines and their CPU time.
func TestReadGroupSamplesARealGroup(t *testing.T) {
	cmd := exec.Command("bash", "-c", "sleep 30 & while :; do :; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	group := readGroup(cmd.Process.Pid)
	var sleep, shell bool
	for _, p := range group {
		sleep = sleep || p.command == "sleep 30"
		shell = shell || (strings.HasPrefix(p.command, "bash -c") && p.cpu > 0)
	}
	if !sleep || !shell {
		t.Fatalf("group %+v, want the sleep and a busy bash", group)
	}
}
