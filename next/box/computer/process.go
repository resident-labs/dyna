// Package computer runs the box's desktop: one X display that is also its VNC server, a window manager and Chromium, and
// the DevTools connection the agent's computer tool drives the page with.
package computer

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"time"
)

const URL = "http://127.0.0.1:9222"

// VNCSocket is where the display serves VNC. Only the box reaches it; dyna computer stream carries it to the owner's view.
const VNCSocket = "/run/dyna/vnc.sock"

// Size is the agent's screen. The desktop never resizes for a viewer, who sees it scaled to fit.
const Size = "1280x800"

const display = ":1"

//go:embed jwmrc
var jwmrc []byte

// desktopArgs make TigerVNC's Xvnc both the display and its VNC server, so it knows exactly what changed. It serves VNC
// on a Unix socket only, with no password: the socket is inside the box, and the engine checks the owner. A box has no
// GPU, and without GLX Xvnc loads no Mesa: about 17 MiB instead of 60, and Chromium draws in software either way. The
// view takes continuous updates, so the frame cap, not the round trip, sets its rate; 60 costs Xvnc about 9% of a core
// under motion where 30 costs 7%.
func desktopArgs(socket string) []string {
	return []string{display, "-geometry", Size, "-depth", "24", "-rfbunixpath", socket, "-rfbport", "-1", "-SecurityTypes", "None",
		"-AlwaysShared", "-AcceptSetDesktopSize=0", "-FrameRate", "60", "-extension", "GLX"}
}

// Profile is Chromium's profile, in the box's volume, so it lasts across restarts.
const Profile = "/box/computer"

// extensions are the image's unpacked Chromium extensions, uBlock Origin Lite and I still don't care about cookies.
const extensions = "/opt/dyna/extensions/ubol,/opt/dyna/extensions/isdcac"

// browserArgs open Chromium in a window on the display, with DevTools on the port the agent's actions use, and with the
// image's extensions. A managed policy cannot install an unpacked extension, and this Chromium, 156, loads one from
// --load-extension without the DisableLoadExtensionCommandLineSwitch override that Chrome 137 and later need.
func browserArgs(profile string) []string {
	return []string{
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=" + profile,
		"--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--password-store=basic",
		"--hide-crash-restore-bubble", "--start-maximized",
		"--load-extension=" + extensions, "about:blank",
	}
}

// clearProfileLock removes the lock a killed Chromium leaves in its profile, unless a Chromium on this host still holds
// it. The lock names the host and process that took it, and a restarted box is another container with another
// hostname, so Chromium takes the old lock for another computer's and asks whether to unlock it instead of starting.
// Its cookie and socket go with it. The caller has no Chromium of the desktop running.
func clearProfileLock(profile string) error {
	lock := filepath.Join(profile, "SingletonLock")
	owner, err := os.Readlink(lock)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil {
		host, _ := os.Hostname()
		if at := strings.LastIndex(owner, "-"); at > 0 && owner[:at] == host {
			if pid, err := strconv.Atoi(owner[at+1:]); err == nil && syscall.Kill(pid, 0) == nil {
				return nil
			}
		}
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if err := os.Remove(filepath.Join(profile, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// cgroup moves the agent out of the root and creates a half-memory child.
func cgroup(root string) (*os.File, error) {
	limit, err := os.ReadFile(filepath.Join(root, "memory.max"))
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(limit))
	if value == "max" {
		return nil, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 1 {
		return nil, fmt.Errorf("invalid memory.max: %s", value)
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

// program is one running part of the desktop. done closes when it has exited.
type program struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *program) kill() {
	if p != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Process owns the desktop until its context is cancelled: the display, then the window manager and Chromium on it, all
// in the computer cgroup. Nothing runs until the first Start.
type Process struct {
	mu      sync.Mutex
	display *program
	wm      *program
	browser *program
	apps    map[*program]struct{} // what launch started
	stopped *program              // the display Stop ended, whose exit is no news
	exit    string
	ooms    uint64 // the computer cgroup's OOM kills when a program last exited
	root    string // the box's cgroup
	socket  string // the display's VNC socket; the window manager's config goes beside it
	tmp     string // where X keeps its lock and socket
	profile string // Chromium's profile
	once    sync.Once
	group   *os.File
	notify  func(bool)
}

func NewProcess() *Process {
	return &Process{root: "/sys/fs/cgroup", socket: VNCSocket, tmp: "/tmp", profile: Profile}
}

func (p *Process) setup() {
	p.once.Do(func() {
		var err error
		p.group, err = cgroup(p.root)
		if err != nil {
			log.Printf("computer: cgroup limit unavailable: %v", err)
		}
		p.ooms = oomCount(p.root)
	})
}

// Start returns a previous exit report once, and starts the parts of the desktop that are not running. A Chromium the
// owner closed opens again on the next use.
func (p *Process) Start(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	report := p.exit
	p.exit = ""
	p.setup()
	if p.display == nil {
		if err := p.startDisplay(ctx); err != nil {
			p.exit = report
			return "", err
		}
	}
	var err error
	if p.wm == nil {
		config := filepath.Join(filepath.Dir(p.socket), "jwmrc")
		if err = os.WriteFile(config, jwmrc, 0644); err == nil {
			p.wm, err = p.spawn(ctx, os.Stderr, "jwm", "-f", config)
		}
	}
	if err == nil && p.browser == nil {
		if err = clearProfileLock(p.profile); err == nil {
			p.browser, err = p.spawn(ctx, os.Stderr, "chromium", browserArgs(p.profile)...)
		}
	}
	if err != nil {
		p.exit = report
		return "", err
	}
	return report, nil
}

// startDisplay starts Xvnc and waits for its VNC socket, which it opens once the display takes clients. A display that
// was killed leaves its lock and sockets, and the lock's process ID may belong to another process after a restart, so
// they go first: only this process runs display :1 in the box.
func (p *Process) startDisplay(ctx context.Context) error {
	for _, stale := range []string{p.socket, filepath.Join(p.tmp, ".X1-lock"), filepath.Join(p.tmp, ".X11-unix", "X1")} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.socket), 0755); err != nil {
		return err
	}
	display, err := p.spawn(ctx, os.Stderr, "Xvnc", desktopArgs(p.socket)...)
	if err != nil {
		return err
	}
	p.display = display
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if _, err := os.Stat(p.socket); err == nil {
			break
		}
		select {
		case <-display.done:
			p.display = nil
			return errors.New("the desktop exited as it started")
		case <-wait.Done():
			p.display = nil
			display.kill()
			return errors.New("the desktop did not start in 10 seconds")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if p.notify != nil {
		p.notify(true)
	}
	return nil
}

// Launch starts command with sh on the running desktop and does not wait for it, as the agent's launch action. It runs
// in the computer cgroup and stops with the desktop. Its output goes to a log of its own in the temporary directory,
// whose path it returns.
func (p *Process) Launch(ctx context.Context, command string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.display == nil {
		return "", errors.New("the desktop is not running")
	}
	log, err := os.CreateTemp("", "dyna-launch-*.log")
	if err != nil {
		return "", err
	}
	defer log.Close()
	app, err := p.spawn(ctx, log, "sh", "-c", command)
	if err != nil {
		return "", err
	}
	if p.apps == nil {
		p.apps = map[*program]struct{}{}
	}
	p.apps[app] = struct{}{}
	return log.Name(), nil
}

// Stop stops the desktop and what runs on it, for the agent's stop action. The next Start starts it again.
func (p *Process) Stop() {
	p.mu.Lock()
	display := p.display
	p.stopped = display
	p.mu.Unlock()
	if display != nil {
		display.kill()
		<-display.done
	}
	// The display's watch stops the rest. Wait for it, so the next Start finds nothing running.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		p.mu.Lock()
		gone := p.display == nil && p.wm == nil && p.browser == nil && len(p.apps) == 0
		p.mu.Unlock()
		if gone {
			return
		}
	}
}

// spawn starts one program of the desktop in its own process group in the computer cgroup, and watches it. The caller
// holds p.mu.
func (p *Process) spawn(ctx context.Context, out io.Writer, name string, args ...string) (*program, error) {
	command := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		return cmd
	}
	cmd := command()
	if p.group != nil {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(p.group.Fd())
	}
	err := cmd.Start()
	if p.group != nil && (errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EINVAL)) {
		log.Printf("computer: cgroup limit unavailable: %v", err)
		_ = p.group.Close()
		p.group = nil
		cmd = command()
		err = cmd.Start()
	}
	if err != nil {
		return nil, err
	}
	started := &program{name: name, cmd: cmd, done: make(chan struct{})}
	go p.watch(ctx, started)
	return started, nil
}

// watch waits for a program to exit. The display takes the rest of the desktop with it. An exit nobody asked for is
// reported on the next Start, unless the display went first and stopped the program. The programs share one cgroup, so
// an OOM kill counts for the first exit after it.
func (p *Process) watch(ctx context.Context, exited *program) {
	err := exited.cmd.Wait()
	close(exited.done)
	p.mu.Lock()
	defer p.mu.Unlock()
	count := oomCount(p.root)
	killed := count > p.ooms
	p.ooms = count
	what := "Chromium"
	switch exited {
	case p.display:
		p.display = nil
		p.wm.kill()
		p.browser.kill()
		for app := range p.apps {
			app.kill()
		}
		if p.notify != nil {
			p.notify(false)
		}
		what = "The desktop"
	case p.wm:
		p.wm = nil
		return
	case p.browser:
		p.browser = nil
		if p.display == nil {
			return
		}
	default:
		delete(p.apps, exited)
		return
	}
	if ctx.Err() != nil || exited == p.stopped {
		return
	}
	if killed {
		p.exit = what + " was killed by the computer memory limit"
	} else if err != nil {
		p.exit = what + " exited: " + err.Error()
	} else {
		p.exit = what + " exited"
	}
}

// Close stops the desktop and waits for its programs to exit.
func (p *Process) Close() {
	p.mu.Lock()
	running := []*program{p.browser, p.wm, p.display}
	for app := range p.apps {
		running = append(running, app)
	}
	group := p.group
	p.mu.Unlock()
	for _, program := range running {
		if program != nil {
			program.kill()
			<-program.done
		}
	}
	if group != nil {
		_ = group.Close()
	}
}

// Serve starts the private agent socket and returns its cleanup function.
func Serve(ctx context.Context, socket string, errs chan<- error, notify func(bool)) (func(), error) {
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
	desktop := NewProcess()
	desktop.notify = notify
	desktop.setup()
	server := &http.Server{Handler: desktop.Handler(ctx)}
	go func() { errs <- server.Serve(listener) }()
	return func() {
		_ = server.Close()
		desktop.Close()
		_ = os.Remove(socket)
	}, nil
}

// StartRequest is the body of a launch: a command for sh.
type StartRequest struct {
	Launch string `json:"launch,omitempty"`
}

// Handler serves the private in-box endpoint: POST /computer starts the desktop, /computer/launch starts the desktop and
// then a command on it, and /computer/stop stops it. Every computer action but stop starts the desktop through it first.
func (p *Process) Handler(ctx context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var started Started
		var err error
		switch r.URL.Path {
		case "/computer/stop":
			p.Stop()
		case "/computer", "/computer/launch":
			var req StartRequest
			if r.URL.Path == "/computer/launch" {
				if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Launch == "" {
					http.Error(w, "a launch needs a command", http.StatusBadRequest)
					return
				}
			}
			started.URL = URL
			started.Exit, err = p.Start(ctx)
			if err == nil && req.Launch != "" {
				started.Log, err = p.Launch(ctx, req.Launch)
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(started)
	})
}
