//go:build linux

package shell

import (
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// lowPriority is the nice value of a command in the box. A box's CPU cap is shared by all its processes, and a command that
// fills it would otherwise starve the box agent. Nice 19 weighs 15 against the agent's 1024, and stays an ordinary task to the rest of
// the host, unlike SCHED_IDLE, which makes the host treat a CPU running only such tasks as idle.
const lowPriority = 19

// StartLowPriority starts cmd at nice 19, and so are its children. On Linux setpriority on pid 0 sets the calling thread, and a
// thread's children inherit its nice value, so the thread sets it and then starts the command, with no window in which the command runs
// at normal priority. The thread is never unlocked, so it ends with the goroutine and takes the value with it. If the value cannot be
// set, the command starts as usual.
func StartLowPriority(cmd *exec.Cmd) error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		_ = unix.Setpriority(unix.PRIO_PROCESS, 0, lowPriority)
		started <- cmd.Start()
	}()
	return <-started
}
