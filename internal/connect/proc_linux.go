package connect

import (
	"os/exec"
	"syscall"
)

// dieWithParent asks the kernel to stop the child when serve exits, even
// on a crash or SIGKILL.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
