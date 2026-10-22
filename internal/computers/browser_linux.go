//go:build linux

package computers

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A browser daemon can outlive its CLI and browser. Include only PIDs whose
// environment identifies this private socket directory, never an owner daemon.
func browserDaemonPIDs(id string) map[int]string {
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	out := map[int]string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			addBrowserDaemon(out, pid, root)
		}
		return nil
	})
	return out
}

// readProcessEnviron is a variable so a test can simulate the empty reads.
var readProcessEnviron = func(pid int) ([]byte, error) {
	return os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
}

// addBrowserDaemon records the process when it is a browser daemon of this
// socket directory. It reports pending when the environment still reads as
// empty on a process that started moments ago: /proc shows no environment in
// the first instant after exec, so the answer is not known yet.
func addBrowserDaemon(out map[int]string, pid int, root string) (pending bool) {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	env, err := readProcessEnviron(pid)
	if err != nil {
		return false
	}
	info, ok := processInfo(pid)
	if !ok {
		return false
	}
	if len(env) == 0 {
		return info.state != "Z" && startedRecently(info.start)
	}
	if envValue(strings.Split(string(env), "\x00"), "AGENT_BROWSER_SOCKET_DIR") == root && envValue(strings.Split(string(env), "\x00"), "AGENT_BROWSER_DAEMON") == "1" {
		out[pid] = info.start
	}
	return false
}

// startedRecently compares a process start time (clock ticks since boot, 100
// per second) with the uptime.
func startedRecently(start string) bool {
	ticks, err := strconv.ParseInt(start, 10, 64)
	uptime, uerr := os.ReadFile("/proc/uptime")
	fields := strings.Fields(string(uptime))
	if err != nil || uerr != nil || len(fields) == 0 {
		return false
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	return err == nil && seconds*100-float64(ticks) < 500
}

// findBrowserDaemons adds a scan of every process to the pid files. A daemon
// writes its pid file only after it starts, so a stop right after start would
// otherwise leave it running. A process whose environment is not readable yet
// is read again, for at most 200 ms, until it is readable or gone.
func findBrowserDaemons(id string) map[int]string {
	out := browserDaemonPIDs(id)
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	pending := map[int]bool{}
	for pid := range processes() {
		if addBrowserDaemon(out, pid, root) {
			pending[pid] = true
		}
	}
	for deadline := time.Now().Add(200 * time.Millisecond); len(pending) > 0 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for pid := range pending {
			if !addBrowserDaemon(out, pid, root) {
				delete(pending, pid)
			}
		}
	}
	return out
}

// Give the tool time to close Chrome and remove its temporary profile. SIGTERM
// every owned process first, so each browser gets a graceful stop with the
// short timeout before the fallback kills only the selected processes with
// unchanged start times.
func stopBrowserDaemons(id string) {
	roots := findBrowserDaemons(id)
	if len(roots) == 0 {
		return
	}
	owned := descendants(processes(), roots)
	signal := func(pid int, start string, sig unix.Signal) {
		if info, ok := processInfo(pid); ok && info.start == start && info.state != "Z" {
			_ = unix.Kill(pid, sig)
		}
	}
	for pid, start := range owned {
		signal(pid, start, unix.SIGTERM)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for pid, start := range owned {
			if info, ok := processInfo(pid); ok && info.start == start && info.state != "Z" {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for pid, start := range owned {
		signal(pid, start, unix.SIGKILL)
	}
}
