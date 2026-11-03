package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCgroupLimitAndFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("1000"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("123\n456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := cgroup(root)
	if err != nil {
		t.Fatal(err)
	}
	group.Close()
	moved, err := os.ReadFile(filepath.Join(root, "agent/cgroup.procs"))
	if err != nil || string(moved) != "456" {
		t.Fatalf("second process not moved: %q, %v", moved, err)
	}
	value, err := os.ReadFile(filepath.Join(root, "computer/memory.max"))
	if err != nil || string(value) != "500" {
		t.Fatalf("memory limit %q: %v", value, err)
	}
	if _, err := cgroup(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing cgroup must fall back")
	}
}

func TestCgroupUnlimitedMemory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("max\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := cgroup(root)
	if err != nil || group != nil {
		if group != nil {
			group.Close()
		}
		t.Fatalf("unlimited memory: group=%v err=%v", group, err)
	}
	if _, err := os.Stat(filepath.Join(root, "computer")); !os.IsNotExist(err) {
		t.Fatalf("unexpected computer cgroup: %v", err)
	}
}

func TestLaunchArgs(t *testing.T) {
	browser := strings.Join(browserArgs(Profile), " ")
	for _, flag := range []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=/box/computer", "--disable-dev-shm-usage", "--no-first-run", "--disable-features=DisableLoadExtensionCommandLineSwitch", "--load-extension=/opt/dyna/extensions/ubol,/opt/dyna/extensions/isdcac", "about:blank"} {
		if !strings.Contains(browser, flag) {
			t.Fatalf("missing %s", flag)
		}
	}
	if n := strings.Count(browser, "--load-extension"); n != 1 || strings.Contains(browser, "--load-extension= ") {
		t.Fatalf("want one non-empty --load-extension, got %d in %s", n, browser)
	}
	if strings.Contains(browser, "headless") {
		t.Fatal("Chromium must open a window on the desktop")
	}
	desktop := strings.Join(desktopArgs(VNCSocket), " ")
	for _, flag := range []string{":1 ", "-geometry 1280x800", "-rfbunixpath /run/dyna/vnc.sock", "-rfbport -1", "-SecurityTypes None", "-AcceptSetDesktopSize=0"} {
		if !strings.Contains(desktop, flag) {
			t.Fatalf("missing %s", flag)
		}
	}
}

// fakeDesktop puts an Xvnc, a jwm and a chromium on PATH. Xvnc makes its VNC socket and waits; the others write their
// arguments and wait. With oom, the first chromium exits at once and counts an OOM kill.
func fakeDesktop(t *testing.T, oom bool) (*Process, string) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "computer"), 0700); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(root, "started")
	if !oom {
		if err := os.WriteFile(started, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{
		"Xvnc":     "#!/bin/sh\nwhile [ \"$1\" != -rfbunixpath ]; do shift; done\ntouch \"$2\"\nexec sleep 600\n",
		"jwm":      "#!/bin/sh\necho \"$@\" > " + filepath.Join(root, "jwm.args") + "\nexec sleep 600\n",
		"chromium": fmt.Sprintf("#!/bin/sh\necho \"$DISPLAY $*\" > %q\nif [ ! -f %q ]; then touch %q; echo 'oom_kill 1' > %q; exit 1; fi\nexec sleep 600\n", filepath.Join(root, "chromium.args"), started, started, filepath.Join(root, "computer/memory.events")),
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(filepath.Join(tmp, ".X11-unix"), 0700); err != nil {
		t.Fatal(err)
	}
	// A killed display's lock and socket.
	for _, stale := range []string{filepath.Join(tmp, ".X1-lock"), filepath.Join(tmp, ".X11-unix", "X1")} {
		if err := os.WriteFile(stale, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Process{root: root, socket: filepath.Join(root, "run", "vnc.sock"), tmp: tmp, profile: filepath.Join(root, "profile")}
	if err := os.Mkdir(p.profile, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, root
}

func TestDesktopStartsOnceAndReportsAChromiumExitOnce(t *testing.T) {
	p, root := fakeDesktop(t, true)
	var states []bool
	p.notify = func(running bool) { states = append(states, running) }
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", ".X1-lock")); !os.IsNotExist(err) {
		t.Fatalf("a stale X lock stays: %v", err)
	}
	waitFor(t, func() bool { args, _ := os.ReadFile(filepath.Join(root, "jwm.args")); return len(args) > 0 })
	if args, err := os.ReadFile(filepath.Join(root, "jwm.args")); err != nil || !strings.Contains(string(args), "-f "+filepath.Join(root, "run", "jwmrc")) {
		t.Fatalf("jwm args %q: %v", args, err)
	}
	if config, err := os.ReadFile(filepath.Join(root, "run", "jwmrc")); err != nil || !bytes.Contains(config, []byte("<TaskList")) {
		t.Fatalf("jwm config: %v", err)
	}
	p.mu.Lock()
	display := p.display
	p.mu.Unlock()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.browser == nil })
	report, err := p.Start(context.Background())
	if err != nil || report != "Chromium was killed by the computer memory limit" {
		t.Fatalf("report %q: %v", report, err)
	}
	waitFor(t, func() bool { args, _ := os.ReadFile(filepath.Join(root, "chromium.args")); return len(args) > 0 })
	if args, err := os.ReadFile(filepath.Join(root, "chromium.args")); err != nil || !strings.HasPrefix(string(args), ":1 ") {
		t.Fatalf("Chromium is not on the display: %q, %v", args, err)
	}
	report, err = p.Start(context.Background())
	if err != nil || report != "" {
		t.Fatalf("second report %q: %v", report, err)
	}
	p.mu.Lock()
	same := p.display == display
	p.mu.Unlock()
	if !same || len(states) != 1 || !states[0] {
		t.Fatalf("the display started again: same=%v states=%v", same, states)
	}
}

func TestDesktopExitStopsItsProgramsAndReports(t *testing.T) {
	p, _ := fakeDesktop(t, false)
	var states []bool
	p.notify = func(running bool) { states = append(states, running) }
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	display, wm, browser := p.display, p.wm, p.browser
	p.mu.Unlock()
	display.kill()
	<-wm.done
	<-browser.done
	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.display == nil && p.wm == nil && p.browser == nil
	})
	report, err := p.Start(context.Background())
	if err != nil || report != "The desktop exited: signal: killed" {
		t.Fatalf("report %q: %v", report, err)
	}
	if len(states) != 3 || !states[0] || states[1] || !states[2] {
		t.Fatalf("states %v", states)
	}
}

// A restarted box is another container with another hostname, and the profile in its volume still holds the old
// container's lock. Chromium starts on it cleanly; a lock a live Chromium of this host holds stays.
func TestAProfileLockedForAnotherHostStartsCleanly(t *testing.T) {
	p, root := fakeDesktop(t, false)
	lock := map[string]string{"SingletonLock": "526aeb5bbd13-66", "SingletonCookie": "6633711284523391337", "SingletonSocket": "/tmp/.org.chromium.Chromium.x/SingletonSocket"}
	for name, target := range lock {
		if err := os.Symlink(target, filepath.Join(p.profile, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name := range lock {
		if _, err := os.Lstat(filepath.Join(p.profile, name)); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", name, err)
		}
	}
	waitFor(t, func() bool { args, _ := os.ReadFile(filepath.Join(root, "chromium.args")); return len(args) > 0 })
	if args, _ := os.ReadFile(filepath.Join(root, "chromium.args")); !strings.Contains(string(args), "--user-data-dir="+p.profile) {
		t.Fatalf("Chromium args %q", args)
	}

	host, _ := os.Hostname()
	live := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(live, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, os.Getpid()), filepath.Join(live, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := clearProfileLock(live); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(live, "SingletonLock")); err != nil {
		t.Fatalf("a live Chromium's lock went: %v", err)
	}
	if err := clearProfileLock(filepath.Join(t.TempDir(), "none")); err != nil {
		t.Fatalf("a profile with no lock: %v", err)
	}
}

// A launch runs on the desktop with a log of its own, and stop ends it with the desktop.
func TestLaunchLogsAndStopEndsEverything(t *testing.T) {
	p, _ := fakeDesktop(t, false)
	var states []bool
	p.notify = func(running bool) { states = append(states, running) }
	handler := p.Handler(context.Background())
	post := func(path, body string) (Started, int) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		var started Started
		_ = json.Unmarshal(w.Body.Bytes(), &started)
		return started, w.Code
	}
	if _, code := post("/computer/launch", `{}`); code != http.StatusBadRequest {
		t.Fatalf("a launch without a command: %d", code)
	}
	started, code := post("/computer/launch", `{"launch":"echo launched on $DISPLAY; exec sleep 600"}`)
	if code != http.StatusOK || started.URL != URL || started.Log == "" {
		t.Fatalf("launch %d %+v", code, started)
	}
	t.Cleanup(func() { _ = os.Remove(started.Log) })
	waitFor(t, func() bool { out, _ := os.ReadFile(started.Log); return string(out) == "launched on :1\n" })
	if _, code := post("/computer/stop", ""); code != http.StatusOK {
		t.Fatalf("stop %d", code)
	}
	p.mu.Lock()
	left := p.display != nil || p.wm != nil || p.browser != nil || len(p.apps) > 0
	p.mu.Unlock()
	if left || len(states) != 2 || states[1] {
		t.Fatalf("after stop: running=%v states=%v", left, states)
	}
	if started, code := post("/computer", ""); code != http.StatusOK || started.Exit != "" {
		t.Fatalf("start after stop %d %+v: a stop is no exit to report", code, started)
	}
}

func TestStreamCarriesBothWaysUntilTheViewCloses(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.Copy(io.Discard, conn)
	}()
	in, view := io.Pipe()
	out, screen := io.Pipe()
	ended := make(chan error, 1)
	go func() { ended <- Stream(context.Background(), socket, in, screen) }()
	greeting := make([]byte, 12)
	if _, err := io.ReadFull(out, greeting); err != nil || string(greeting) != "RFB 003.008\n" {
		t.Fatalf("greeting %q: %v", greeting, err)
	}
	if _, err := view.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	_ = view.Close()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its view")
	}
	if err := Stream(context.Background(), filepath.Join(t.TempDir(), "none.sock"), nil, io.Discard); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no desktop: %v", err)
	}
}

func TestOOMCount(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "computer"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "computer/memory.events"), []byte("oom 3\noom_kill 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := oomCount(root); got != 2 {
		t.Fatalf("oom kills = %d", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
