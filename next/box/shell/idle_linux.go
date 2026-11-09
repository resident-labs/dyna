//go:build linux

package shell

import (
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// StartIdle starts cmd under the SCHED_IDLE policy, so it runs only on CPU that nothing else in its cgroup wants, and so do its
// children. A box's CPU cap is shared by all its processes, and a command that fills it would otherwise starve the box agent.
// The policy is set on a thread of its own and inherited by the process the thread starts, with no window in which the command
// runs without it. The thread is never unlocked, so it ends with the goroutine and takes the policy with it. If the policy
// cannot be set, the command starts as usual.
func StartIdle(cmd *exec.Cmd) error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		_ = unix.SchedSetAttr(0, &unix.SchedAttr{Size: unix.SizeofSchedAttr, Policy: unix.SCHED_IDLE}, 0)
		started <- cmd.Start()
	}()
	return <-started
}
