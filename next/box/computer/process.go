// Package computer manages the box's Chromium process and DevTools connection.
package computer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const URL = "http://127.0.0.1:9222"

var launchArgs = []string{
	"--headless=new", "--remote-debugging-address=127.0.0.1",
	"--remote-debugging-port=9222", "--user-data-dir=/box/computer",
	"--disable-dev-shm-usage", "--no-first-run", "about:blank",
}

// cgroup moves the agent out of the root and creates a half-memory child.
func cgroup(root string) (*os.File, error) {
	limit, err := os.ReadFile(filepath.Join(root, "memory.max"))
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(limit)), 10, 64)
	if err != nil || n <= 1 {
		return nil, fmt.Errorf("invalid memory.max: %s", strings.TrimSpace(string(limit)))
	}
	for _, name := range []string{"agent", "computer"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	pids, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	for _, pid := range strings.Fields(string(pids)) {
		if _, err := strconv.Atoi(pid); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(root, "agent/cgroup.procs"), []byte(pid), 0644); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+memory"), 0644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "computer/memory.max"), []byte(strconv.FormatInt(n/2, 10)), 0644); err != nil {
		return nil, err
	}
	return os.Open(filepath.Join(root, "computer"))
}

func oomCount(root string) uint64 {
	data, err := os.ReadFile(filepath.Join(root, "computer/memory.events"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			n, _ := strconv.ParseUint(fields[1], 10, 64)
			return n
		}
	}
	return 0
}

// Process owns Chromium until its context is cancelled.
type Process struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	done  chan struct{}
	exit  string
	root  string
	once  sync.Once
	group *os.File
}

func NewProcess() *Process { return &Process{root: "/sys/fs/cgroup"} }

func (p *Process) setup() {
	p.once.Do(func() {
		var err error
		p.group, err = cgroup(p.root)
		if err != nil {
			log.Printf("computer: cgroup limit unavailable: %v", err)
		}
	})
}

// Start returns a previous exit report once and starts Chromium when needed.
func (p *Process) Start(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	report := p.exit
	p.exit = ""
	if p.cmd != nil {
		return report, nil
	}
	p.setup()
	cmd := exec.CommandContext(ctx, "chromium", launchArgs...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if p.group != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(p.group.Fd())
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	before := oomCount(p.root)
	err := cmd.Start()
	if p.group != nil && (errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EINVAL)) {
		log.Printf("computer: cgroup limit unavailable: %v", err)
		_ = p.group.Close()
		p.group = nil
		cmd = exec.CommandContext(ctx, "chromium", launchArgs...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		err = cmd.Start()
	}
	if err != nil {
		p.exit = report
		return "", err
	}
	p.cmd = cmd
	p.done = make(chan struct{})
	done := p.done
	go func() {
		defer close(done)
		err := cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.cmd = nil
		if ctx.Err() != nil {
			return
		}
		if oomCount(p.root) > before {
			p.exit = "Chromium was killed by the computer memory limit"
		} else if err != nil {
			p.exit = "Chromium exited: " + err.Error()
		} else {
			p.exit = "Chromium exited"
		}
	}()
	return report, nil
}

// Close stops Chromium and waits for its process to exit.
func (p *Process) Close() {
	p.mu.Lock()
	cmd, done, group := p.cmd, p.done, p.group
	p.mu.Unlock()
	if cmd != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	if group != nil {
		_ = group.Close()
	}
}

// Serve starts the private agent socket and returns its cleanup function.
func Serve(ctx context.Context, socket string, errs chan<- error) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0755); err != nil {
		return nil, err
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	browser := NewProcess()
	browser.setup()
	server := &http.Server{Handler: browser.Handler(ctx)}
	go func() { errs <- server.Serve(listener) }()
	return func() {
		_ = server.Close()
		browser.Close()
		_ = os.Remove(socket)
	}, nil
}

// Handler serves the private in-box start endpoint.
func (p *Process) Handler(ctx context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/computer" {
			http.NotFound(w, r)
			return
		}
		report, err := p.Start(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"url\":%q,\"exit\":%q}\n", URL, report)
	})
}
