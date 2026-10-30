//go:build linux

package agent

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
)

// Guard tracks the shell process groups for turn cancellation.
type Guard struct {
	mu   sync.Mutex
	jobs map[*Job]struct{}
}

type Job struct {
	guard *Guard
	cmd   *exec.Cmd
}

func NewGuard() *Guard { return &Guard{jobs: make(map[*Job]struct{})} }

func (g *Guard) Start(command, cwd string, stdout, stderr io.Writer) (*Job, error) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	job := &Job{guard: g, cmd: cmd}
	g.mu.Lock()
	g.jobs[job] = struct{}{}
	g.mu.Unlock()
	return job, nil
}

func (j *Job) Wait() error {
	err := j.cmd.Wait()
	_ = j.Stop()
	j.guard.mu.Lock()
	delete(j.guard.jobs, j)
	j.guard.mu.Unlock()
	return err
}

func (j *Job) Stop() error {
	err := syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (g *Guard) StopAll() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var first error
	for job := range g.jobs {
		if err := job.Stop(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
