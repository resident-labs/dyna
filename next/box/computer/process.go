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
// GPU; GLX stays enabled for software-rendered desktop apps. The
// view takes continuous updates, so the frame cap, not the round trip, sets its rate; 60 costs Xvnc about 9% of a core
// under motion where 30 costs 7%.
func desktopArgs(socket string) []string {
	return []string{display, "-geometry", Size, "-depth", "24", "-rfbunixpath", socket, "-rfbport", "-1", "-SecurityTypes", "None",
		"-AlwaysShared", "-AcceptSetDesktopSize=0", "-FrameRate", "60"}
}

// Profile is Chromium's per-box profile, in the box's volume, so it lasts across restarts.
const Profile = "/box/computer"

// SharedProfile is Chromium's working copy while a box holds a shared browser profile lease.
// It is deleted once the profile is checked back in, leaving Profile untouched.
const SharedProfile = "/box/computer-shared"

const (
	defaultIdleTimeout       = 15 * time.Minute
	cleanBrowserCloseTimeout = 5 * time.Second
	checkinTimeout           = 30 * time.Second
)

// Lease identifies a box's active checkout of an owner-level shared browser profile.
type Lease struct {
	Profile    string
	Generation int64
}

// ErrStaleLease reports that the engine refused a checkin because the lease generation no longer matches.
var ErrStaleLease = errors.New("browser lease no longer matches")

// ProfileStore checks shared browser profiles out from and back into the engine.
type ProfileStore interface {
	Checkout(ctx context.Context, dir string) (Lease, error)
	Checkin(ctx context.Context, lease Lease, dir string, discard bool) error
}

// extensions are the image's unpacked Chromium extensions, uBlock Origin Lite and I still don't care about cookies.
const extensions = "/opt/dyna/extensions/ubol,/opt/dyna/extensions/isdcac"

// browserArgs open Chromium in a window on the display, with DevTools on the port the agent's actions use, and with the
// image's extensions. A managed policy cannot install an unpacked extension. This Chromium, 156, loads one from
// --load-extension without the override, but Chrome 137 and later need DisableLoadExtensionCommandLineSwitch, so it is
// passed ahead of the next bump. The extension folders must be writable by the user (see the dyna-base Containerfile).
func browserArgs(profile string) []string {
	return []string{
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=" + profile,
		"--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--password-store=basic",
		"--hide-crash-restore-bubble", "--start-maximized",
		"--disable-features=DisableLoadExtensionCommandLineSwitch", "--load-extension=" + extensions, "about:blank",
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

// Process owns the desktop until Close: the display, then the window manager and Chromium on it, all
// in the computer cgroup. Nothing runs until the first Start.
type Process struct {
	opMu           sync.Mutex // serializes Start, Stop, Close and idle releases
	mu             sync.Mutex
	display        *program
	wm             *program
	browser        *program
	apps           map[*program]struct{} // what launch started
	stopped        *program              // the display Stop ended, whose exit is no news
	stoppedBrowser *program              // the Chromium Stop closed cleanly, whose exit is no news
	exit           string
	ooms           uint64 // the computer cgroup's OOM kills when a program last exited
	root           string // the box's cgroup
	socket         string // the display's VNC socket; the window manager's config goes beside it
	tmp            string // where X keeps its lock and socket
	profile        string // Chromium's per-box profile
	sharedProfile  string // Chromium's checked-out shared profile
	devtoolsURL    string // DevTools endpoint for Browser.close
	store          ProfileStore
	lease          Lease
	browserUnclean bool
	viewers        int
	idleSeq        uint64
	idleTimeout    time.Duration
	closeTimeout   time.Duration
	idleTimer      *time.Timer
	closed         bool
	once           sync.Once
	group          *os.File
	notify         func(bool)
}

func NewProcess() *Process {
	return &Process{
		root:          "/sys/fs/cgroup",
		socket:        VNCSocket,
		tmp:           "/tmp",
		profile:       Profile,
		sharedProfile: SharedProfile,
		devtoolsURL:   URL,
		idleTimeout:   defaultIdleTimeout,
		closeTimeout:  cleanBrowserCloseTimeout,
	}
}

func (p *Process) sharedDir() string {
	if p.sharedProfile != "" {
		return p.sharedProfile
	}
	return SharedProfile
}

func (p *Process) devtoolsBase() string {
	if p.devtoolsURL != "" {
		return p.devtoolsURL
	}
	return URL
}

func (p *Process) browserCloseLimit() time.Duration {
	if p.closeTimeout > 0 {
		return p.closeTimeout
	}
	return cleanBrowserCloseTimeout
}

func (p *Process) idleLimit() time.Duration {
	if p.idleTimeout > 0 {
		return p.idleTimeout
	}
	return defaultIdleTimeout
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

func (p *Process) stopIdleTimerLocked() {
	p.idleSeq++
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
}

func (p *Process) resetIdleTimerLocked() {
	p.stopIdleTimerLocked()
	if p.closed || p.lease.Profile == "" || p.display == nil || p.viewers > 0 {
		return
	}
	seq := p.idleSeq
	p.idleTimer = time.AfterFunc(p.idleLimit(), func() {
		p.stopIfIdle(seq)
	})
}

func (p *Process) stopIfIdle(seq uint64) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.mu.Lock()
	stale := p.closed || p.idleSeq != seq || p.viewers > 0 || p.lease.Profile == "" || p.display == nil
	p.mu.Unlock()
	if stale {
		return
	}
	if err := p.stopLocked(); err != nil {
		log.Printf("computer: idle shared browser release failed: %v", err)
	}
}

// Start returns a previous exit report once, and starts the parts of the desktop that are not running. A Chromium the
// owner closed opens again on the next use.
func (p *Process) Start(ctx context.Context) (string, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.mu.Lock()
	needCheckout := p.display == nil && p.store != nil && p.lease.Profile == ""
	p.mu.Unlock()
	if needCheckout {
		dir := p.sharedDir()
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		lease, err := p.store.Checkout(ctx, dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		if lease.Profile != "" {
			p.mu.Lock()
			p.lease = lease
			p.browserUnclean = false
			p.mu.Unlock()
		}
	}
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
			p.wm, err = p.spawn(os.Stderr, "jwm", "-f", config)
		}
	}
	profileDir := p.profile
	if p.lease.Profile != "" {
		profileDir = p.sharedDir()
	}
	if err == nil && p.browser == nil {
		if err = clearProfileLock(profileDir); err == nil {
			p.browser, err = p.spawn(os.Stderr, "chromium", browserArgs(profileDir)...)
		}
	}
	if err != nil {
		p.exit = report
		return "", err
	}
	p.resetIdleTimerLocked()
	return report, nil
}

// startDisplay starts Xvnc and waits for its VNC socket, which it opens once the display takes clients. A display that
// was killed leaves its lock and sockets, and the lock's process ID may belong to another process after a restart, so
// they go first: only this process runs display :1 in the box.
func (p *Process) startDisplay(ctx context.Context) error {
	if err := os.MkdirAll(runtimeDir(p.socket), 0700); err != nil {
		return err
	}
	if err := os.Chmod(runtimeDir(p.socket), 0700); err != nil {
		return err
	}
	for _, stale := range []string{p.socket, filepath.Join(p.tmp, ".X1-lock"), filepath.Join(p.tmp, ".X11-unix", "X1")} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p.socket), 0755); err != nil {
		return err
	}
	display, err := p.spawn(os.Stderr, "Xvnc", desktopArgs(p.socket)...)
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
	app, err := p.spawn(log, "sh", "-c", command)
	if err != nil {
		return "", err
	}
	if p.apps == nil {
		p.apps = map[*program]struct{}{}
	}
	p.apps[app] = struct{}{}
	p.resetIdleTimerLocked()
	return log.Name(), nil
}

// Stop stops the desktop and what runs on it, for the agent's stop action. When a shared browser profile
// is checked out, Chromium is closed cleanly first and the profile is checked back into the engine.
// The next Start starts the desktop again.
func (p *Process) Stop() error {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	return p.stopLocked()
}

// closeBrowserCleanly asks Chromium to exit via DevTools Browser.close and falls back to SIGTERM.
// It returns true only if Chromium exited before the deadline without being SIGKILLed.
func (p *Process) closeBrowserCleanly(browser *program) bool {
	if browser == nil {
		return true
	}
	limit := p.browserCloseLimit()
	deadline := time.Now().Add(limit)
	dtCtx, cancel := context.WithTimeout(context.Background(), min(2*time.Second, limit))
	closeDone := make(chan error, 1)
	go func() { closeDone <- CloseBrowser(dtCtx, p.devtoolsBase()) }()
	select {
	case <-browser.done:
		cancel()
		return true
	case err := <-closeDone:
		cancel()
		if err == nil {
			// DevTools accepted Browser.close; give Chromium time to flush and exit before trying SIGTERM.
			grace := time.NewTimer(min(time.Second, time.Until(deadline)))
			select {
			case <-browser.done:
				grace.Stop()
				return true
			case <-grace.C:
			}
		}
	case <-time.After(time.Until(deadline)):
		cancel()
		browser.kill()
		<-browser.done
		return false
	}
	// Fall back to SIGTERM on Chromium's main PID so it flushes its profile cleanly.
	_ = syscall.Kill(browser.cmd.Process.Pid, syscall.SIGTERM)
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-browser.done:
		return true
	case <-timer.C:
		browser.kill()
		<-browser.done
		return false
	}
}

func (p *Process) stopLocked() error {
	p.mu.Lock()
	p.stopIdleTimerLocked()
	display := p.display
	browser := p.browser
	lease := p.lease
	p.stopped = display
	if lease.Profile != "" {
		p.stoppedBrowser = browser
	}
	p.mu.Unlock()

	cleanBrowser := true
	if lease.Profile != "" && browser != nil {
		cleanBrowser = p.closeBrowserCleanly(browser)
	}
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
			break
		}
	}
	if lease.Profile == "" || p.store == nil {
		return nil
	}
	p.mu.Lock()
	unclean := p.browserUnclean || !cleanBrowser
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), checkinTimeout)
	defer cancel()
	err := p.store.Checkin(ctx, lease, p.sharedDir(), unclean)
	if err == nil || errors.Is(err, ErrStaleLease) {
		if errors.Is(err, ErrStaleLease) {
			log.Printf("computer: shared browser %s checkin refused (stale lease); discarding local copy", lease.Profile)
		}
		_ = os.RemoveAll(p.sharedDir())
		p.mu.Lock()
		p.lease = Lease{}
		p.browserUnclean = false
		p.mu.Unlock()
		return nil
	}
	return err
}

func (p *Process) releaseUnexpectedLease(lease Lease) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.mu.Lock()
	if p.lease != lease || p.display != nil {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	if p.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkinTimeout)
	defer cancel()
	err := p.store.Checkin(ctx, lease, p.sharedDir(), true)
	if err == nil || errors.Is(err, ErrStaleLease) {
		_ = os.RemoveAll(p.sharedDir())
		p.mu.Lock()
		if p.lease == lease {
			p.lease = Lease{}
			p.browserUnclean = false
		}
		p.mu.Unlock()
	} else {
		log.Printf("computer: releasing shared browser %s after desktop exit: %v", lease.Profile, err)
	}
}

// spawn starts one program of the desktop in its own process group in the computer cgroup, and watches it. The caller
// holds p.mu.
func (p *Process) spawn(out io.Writer, name string, args ...string) (*program, error) {
	command := func() *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Env = desktopEnv(p.socket)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	go p.watch(started)
	return started, nil
}

// watch waits for a program to exit. The display takes the rest of the desktop with it. An exit nobody asked for is
// reported on the next Start, unless the display went first and stopped the program. The programs share one cgroup, so
// an OOM kill counts for the first exit after it.
func (p *Process) watch(exited *program) {
	err := exited.cmd.Wait()
	close(exited.done)
	p.mu.Lock()
	count := oomCount(p.root)
	killed := count > p.ooms
	p.ooms = count
	what := "Chromium"
	var unexpectedLease Lease
	switch exited {
	case p.display:
		p.display = nil
		p.stopIdleTimerLocked()
		p.wm.kill()
		p.browser.kill()
		for app := range p.apps {
			app.kill()
		}
		if p.notify != nil {
			p.notify(false)
		}
		if exited != p.stopped && p.lease.Profile != "" {
			p.browserUnclean = true
			unexpectedLease = p.lease
		}
		what = "The desktop"
	case p.wm:
		p.wm = nil
		p.mu.Unlock()
		return
	case p.browser:
		p.browser = nil
		if killed {
			p.browserUnclean = true
		}
		if exited == p.stoppedBrowser {
			p.mu.Unlock()
			return
		}
		if err != nil && p.lease.Profile != "" {
			p.browserUnclean = true
		}
		if p.display == nil {
			p.mu.Unlock()
			return
		}
	default:
		delete(p.apps, exited)
		p.mu.Unlock()
		return
	}
	if p.closed || exited == p.stopped {
		p.mu.Unlock()
		return
	}
	if killed {
		p.exit = what + " was killed by the computer memory limit"
	} else if err != nil {
		p.exit = what + " exited: " + err.Error()
	} else {
		p.exit = what + " exited"
	}
	p.mu.Unlock()
	if unexpectedLease.Profile != "" {
		go p.releaseUnexpectedLease(unexpectedLease)
	}
}

// Close stops the desktop cleanly (checking in any held shared browser profile) and waits for its programs to exit.
func (p *Process) Close() {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	p.mu.Lock()
	p.closed = true
	p.stopIdleTimerLocked()
	hasLease := p.lease.Profile != ""
	group := p.group
	p.group = nil
	p.mu.Unlock()
	if hasLease {
		if err := p.stopLocked(); err != nil {
			log.Printf("computer: shared browser checkin on close failed: %v", err)
		}
	} else {
		p.mu.Lock()
		running := []*program{p.browser, p.wm, p.display}
		for app := range p.apps {
			running = append(running, app)
		}
		p.mu.Unlock()
		for _, program := range running {
			if program != nil {
				program.kill()
				<-program.done
			}
		}
	}
	if group != nil {
		_ = group.Close()
	}
}

// Serve starts the private agent socket and returns the desktop Process and its cleanup function.
func Serve(ctx context.Context, socket string, errs chan<- error, notify func(bool), store ProfileStore) (*Process, func(), error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0755); err != nil {
		return nil, nil, err
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, nil, err
	}
	desktop := NewProcess()
	desktop.notify = notify
	desktop.store = store
	desktop.setup()
	server := &http.Server{Handler: desktop.Handler(ctx)}
	go func() { errs <- server.Serve(listener) }()
	return desktop, func() {
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
// then a command on it, /computer/stop stops it, and /computer/viewer tracks an open VNC viewer stream.
func (p *Process) Handler(ctx context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/computer/viewer" {
			p.mu.Lock()
			p.viewers++
			p.stopIdleTimerLocked()
			p.mu.Unlock()
			defer func() {
				p.mu.Lock()
				p.viewers--
				if p.viewers == 0 {
					p.resetIdleTimerLocked()
				}
				p.mu.Unlock()
			}()
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			_, _ = io.Copy(io.Discard, r.Body)
			return
		}
		var started Started
		var err error
		switch r.URL.Path {
		case "/computer/stop":
			err = p.Stop()
		case "/computer", "/computer/launch", "/computer/check":
			var req StartRequest
			if r.URL.Path == "/computer/launch" {
				if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Launch == "" {
					http.Error(w, "a launch needs a command", http.StatusBadRequest)
					return
				}
			}
			started.URL = URL
			started.Exit, err = p.Start(ctx)
			if err == nil && r.URL.Path == "/computer/check" {
				started.Log = p.Check(r.Context())
			}
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
