//go:build linux

package workflowsvc

import (
	"os/exec"
	"runtime"
	"syscall"
)

func foregroundParent(cmd *exec.Cmd) func() {
	runtime.LockOSThread()
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
	return runtime.UnlockOSThread
}
