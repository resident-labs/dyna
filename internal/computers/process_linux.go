//go:build linux

package computers

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/proc"
	"dyna-agent/internal/userservices"
)

func startDaemon(ctx context.Context, exe string) error {
	if err := checkDaemonSpawn(exe); err != nil {
		return err
	}
	if err := os.MkdirAll(Root(), 0700); err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if _, err = os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "systemd", "private")); err == nil {
			return startComputerService(ctx)
		}
	}
	cmd := exec.Command(exe, "computers")
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{daemonMarker: "1"})
	f, err := os.OpenFile(filepath.Join(Root(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdin = nil
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = childenv.Clean(cmd).Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// startComputerService uses the installed unit; the Go-test spawn guard stays
// at startDaemon's entry. Tests call this branch only with a fake systemctl.
func startComputerService(ctx context.Context) error {
	if err := userservices.ComputerEnvironment(userservices.Dir(), os.Environ()); err != nil {
		return err
	}
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(startCtx, "systemctl", "--user", "start", userservices.Computers)
	output, err := childenv.Clean(cmd).CombinedOutput()
	if err == nil {
		return nil
	}
	hint := ""
	state := exec.CommandContext(startCtx, "systemctl", "--user", "show", userservices.Computers, "--property=LoadState", "--value")
	if loadState, _ := childenv.Clean(state).Output(); strings.TrimSpace(string(loadState)) == "not-found" {
		hint = "; install it with dyna engine install-services"
	}
	return fmt.Errorf("start computer service%s: %w: %s", hint, err, output)
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startProcess(env []string, logPath string, args ...string) (*process, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return startTracked(cmd)
}

// startTracked starts a prebuilt command the way the daemon owns its
// children: with a death signal, a cleaned environment and reaper tracking.
func startTracked(cmd *exec.Cmd) (*process, error) {
	p := &process{cmd: cmd, done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := startOwned(cmd); err != nil {
			ready <- err
			return
		}
		ready <- nil
		p.err = waitOwned(cmd)
		close(p.done)
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return p, nil
}
func (p *process) alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *process) stop() {
	if !p.alive() {
		return
	}
	_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(500 * time.Millisecond):
		_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
		<-p.done
	}
}

type procInfo struct {
	pid, ppid   int
	start, name string
	state       string
}

func processes() map[int]procInfo {
	inventory := proc.ListStat("/proc")
	out := make(map[int]procInfo, len(inventory))
	for pid, p := range inventory {
		out[pid] = procInfoFrom(p)
	}
	return out
}
func processInfo(pid int) (procInfo, bool) {
	p, err := proc.ReadStat("/proc", pid)
	if err != nil {
		return procInfo{}, false
	}
	return procInfoFrom(p), true
}
func procInfoFrom(p proc.Process) procInfo {
	return procInfo{
		pid: p.PID, ppid: p.ParentPID, start: strconv.FormatUint(p.StartTime, 10),
		name: p.Command, state: p.State,
	}
}
func descendants(all map[int]procInfo, roots map[int]string) map[int]string {
	out := map[int]string{}
	for pid, start := range roots {
		if p, ok := all[pid]; ok && p.start == start {
			out[pid] = start
		}
	}
	for changed := true; changed; {
		changed = false
		for pid, p := range all {
			if _, ok := out[p.ppid]; ok {
				if _, seen := out[pid]; !seen {
					out[pid] = p.start
					changed = true
				}
			}
		}
	}
	return out
}
func rssKiB(pids map[int]string) int64 {
	var total int64
	for pid := range pids {
		f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			fields := strings.Fields(scan.Text())
			if len(fields) > 1 && fields[0] == "VmRSS:" {
				n, _ := strconv.ParseInt(fields[1], 10, 64)
				total += n
				break
			}
		}
		f.Close()
	}
	return total
}

// Destroy releases one registered computer, keeping logs and last.png.
func Destroy(ctx context.Context, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	if _, err := ReadMeta(id); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(ControlSocket(id)); err == nil {
		var reply Stopped
		if err := NewClient(id).Call(ctx, Request{Op: "destroy"}, &reply); err == nil {
			return nil
		}
		// The daemon is hung, refused, or already gone. Stop the processes from
		// here, so a failed call cannot leave the browser running.
		offline, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return destroyOffline(offline, id)
	}
	return destroyOffline(ctx, id)
}

func destroyOffline(ctx context.Context, id string) error {
	m, err := ReadMeta(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Keep unsaved login state available for a later retry. The lock covers the
	// lease read and the profile removal, so a daemon still cleaning up cannot
	// release the same lease twice.
	err = withBrowserCleanup(ctx, id, func() error {
		stopProfileBrowsers(id)
		if err := saveBrowserProfile(ctx, id); err != nil {
			return err
		}
		return os.RemoveAll(filepath.Join(Dir(id), "profiles"))
	})
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), m.WaylandDisplay))
	// This path runs when the daemon is gone or hung, so the recorded room
	// scope would otherwise keep its processes with no record left to find it.
	killRecordedScope(id)
	if err = os.RemoveAll(RuntimeDir(id)); err != nil {
		return err
	}
	stopBrowserDaemons(id)
	_ = os.RemoveAll(browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id))
	s := savedStatus(id)
	s.SessionID = id
	s.State = StateDestroyed
	s.RSSKiB = 0
	s.Stream = Stream{}
	s.StreamRSSKiB = 0
	s.Viewers = 0
	s.Control = ControlAgent
	s.Reason = "session destroyed while daemon was stopped"
	return saveStatus(s)
}

// ProcessStart returns the start time of a running process in clock ticks since boot.
// A pid file with this value tells the process it names from a later process that reused the pid.
func ProcessStart(pid int) (string, bool) {
	p, ok := processInfo(pid)
	return p.start, ok && p.state != "Z"
}
