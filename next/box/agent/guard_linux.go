//go:build linux

package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// Guard starts each shell job directly inside a dedicated child cgroup.
type Guard struct {
	root string
	mu   sync.Mutex
	next uint64
	jobs map[*Job]struct{}
}

type Job struct {
	guard *Guard
	cmd   *exec.Cmd
	path  string
	kept  bool
}

// PrepareCgroups moves box processes out of the controller root before enabling it.
func PrepareCgroups(root string) (*Guard, error) {
	agent := filepath.Join(root, "agent")
	jobs := filepath.Join(root, "jobs")
	if err := os.MkdirAll(agent, 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(jobs, 0755); err != nil {
		return nil, err
	}
	for _, pid := range []int{1, os.Getpid()} {
		if err := os.WriteFile(filepath.Join(agent, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0200); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+cpu +memory +pids"), 0200); err != nil {
		return nil, err
	}
	return NewGuard(jobs), nil
}

func NewGuard(root string) *Guard { return &Guard{root: root, jobs: make(map[*Job]struct{})} }

func (g *Guard) Start(command, cwd string, stdout, stderr io.Writer) (*Job, error) {
	g.mu.Lock()
	g.next++
	path := filepath.Join(g.root, "job-"+strconv.FormatUint(g.next, 10))
	g.mu.Unlock()
	if err := os.Mkdir(path, 0755); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	defer syscall.Close(fd)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir, cmd.Stdout, cmd.Stderr = cwd, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return nil, err
	}
	job := &Job{guard: g, cmd: cmd, path: path}
	g.mu.Lock()
	g.jobs[job] = struct{}{}
	g.mu.Unlock()
	return job, nil
}

func (j *Job) Wait() error {
	err := j.cmd.Wait()
	j.guard.mu.Lock()
	delete(j.guard.jobs, j)
	j.guard.mu.Unlock()
	// A background descendant may still exist after the shell exits.
	if killErr := os.WriteFile(filepath.Join(j.path, "cgroup.kill"), []byte("1"), 0200); killErr != nil && err == nil {
		err = fmt.Errorf("stop job cgroup: %w", killErr)
	}
	if removeErr := os.Remove(j.path); removeErr != nil && err == nil {
		err = removeErr
	}
	return err
}

func (j *Job) Stop() error {
	return os.WriteFile(filepath.Join(j.path, "cgroup.kill"), []byte("1"), 0200)
}

func (j *Job) Keep() {
	j.guard.mu.Lock()
	j.kept = true
	j.guard.mu.Unlock()
}

// EndTurn stops every job that the caller did not keep.
func (g *Guard) EndTurn() error {
	g.mu.Lock()
	var jobs []*Job
	for job := range g.jobs {
		if !job.kept {
			jobs = append(jobs, job)
		}
	}
	g.mu.Unlock()
	var first error
	for _, job := range jobs {
		if err := job.Stop(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
