//go:build linux

package computers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/childreaper"
)

var commandChildren = struct {
	sync.Mutex
	pids map[int]bool
}{pids: map[int]bool{}}
var childChanged = make(chan struct{}, 1)

func notifyChildChange() {
	select {
	case childChanged <- struct{}{}:
	default:
	}
}
func startOwned(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	commandChildren.Lock()
	defer commandChildren.Unlock()
	if err := childenv.Clean(cmd).Start(); err != nil {
		return err
	}
	commandChildren.pids[cmd.Process.Pid] = true
	return nil
}
func waitOwned(cmd *exec.Cmd) error {
	err := cmd.Wait()
	commandChildren.Lock()
	delete(commandChildren.pids, cmd.Process.Pid)
	commandChildren.Unlock()
	notifyChildChange()
	return err
}
func runOwned(cmd *exec.Cmd) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := startOwned(cmd); err != nil {
		return err
	}
	return waitOwned(cmd)
}
func combinedOwned(cmd *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := runOwned(cmd)
	return output.Bytes(), err
}

func reapExitedChildren() {
	commandChildren.Lock()
	defer commandChildren.Unlock()
	childreaper.Reap(func(pid int) bool { return commandChildren.pids[pid] })
}
func reapUnownedPID(pid int) {
	commandChildren.Lock()
	defer commandChildren.Unlock()
	if commandChildren.pids[pid] {
		return
	}
	var status unix.WaitStatus
	_, _ = unix.Wait4(pid, &status, unix.WNOHANG, nil)
}
func startChildReaper() func() {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGCHLD)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				reapExitedChildren()
				return
			case <-sig:
				reapExitedChildren()
			case <-childChanged:
				reapExitedChildren()
			}
		}
	}()
	return func() { signal.Stop(sig); cancel(); <-done }
}
