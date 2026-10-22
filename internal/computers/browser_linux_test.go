//go:build linux

package computers

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/store"
)

// The live agent-browser checks are two tests, so a loaded host stays under
// the 10 s limit: routing for one computer (with its window), then session-name
// isolation across two computers (without waiting for their windows).
func agentBrowserTools(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("agent-browser")
	if err != nil {
		t.Skip("live browser test needs agent-browser")
	}
	for _, tool := range []string{"labwc", "wlr-randr", "Xwayland"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("live browser test needs %s", tool)
		}
	}
	if _, err := os.Stat("/usr/lib/chromium/chromium"); err != nil {
		t.Skip("live browser test needs Chromium")
	}
	return binary
}

type agentBrowserLaunch struct {
	client *Client
	meta   Meta
	cmd    *exec.Cmd
	output *bytes.Buffer
}

// startAgentBrowser registers a computer and starts agent-browser on it with
// stale owner routing settings, which the computer's environment must replace.
// It does not wait: callers start every browser before waiting for any.
func startAgentBrowser(t *testing.T, ctx context.Context, binary, prefix string, headed bool) *agentBrowserLaunch {
	t.Helper()
	client := NewClient(testSessionID(t, prefix))
	m, err := client.Register(ctx, "browser-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.Stop(cleanup)
	})
	base := mergeEnvironment(os.Environ(), map[string]string{"AGENT_BROWSER_CDP": "9222", "AGENT_BROWSER_AUTO_CONNECT": "1", "AGENT_BROWSER_PROFILE": "/owner/profile", "AGENT_BROWSER_SOCKET_DIR": "/owner/sockets"})
	env, err := Environment(m, base)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise agent-browser's real environment routing directly. The cookie
	// round-trip test covers the Chromium launcher.
	env = mergeEnvironment(env, map[string]string{
		"TOKIO_WORKER_THREADS":          "2",
		"AGENT_BROWSER_EXECUTABLE_PATH": "/usr/lib/chromium/chromium",
		"AGENT_BROWSER_ARGS":            "--ozone-platform=wayland,--no-sandbox,--disable-gpu,--disable-dev-shm-usage",
	})
	// Use an absolute tool path and the SAME explicit session name for every
	// computer. This reproduces Porch's bypass of ordinary PATH shims.
	args := []string{"--session", "shared"}
	if headed {
		args = append(args, "--headed")
	}
	cmd := exec.CommandContext(ctx, binary, append(args, "open", "data:text/html,<title>"+m.SessionID+"</title><h1>Computer browser proof</h1>")...)
	cmd.Env = env
	output := new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &agentBrowserLaunch{client, m, cmd, output}
}

// routing checks that the launch's daemon is the computer's own, on the
// computer's display, with the computer's profile. It returns the daemon PID
// and the profile.
func (l *agentBrowserLaunch) routing(t *testing.T) (int, string) {
	t.Helper()
	daemon := l.started(t)
	return daemon, l.profile(t)
}

// started waits for the launch to finish and checks that its daemon is the
// computer's own, on the computer's display. It returns the daemon PID.
func (l *agentBrowserLaunch) started(t *testing.T) int {
	t.Helper()
	if err := l.cmd.Wait(); err != nil {
		t.Fatalf("agent-browser: %v: %s", err, l.output)
	}
	pids := browserDaemonPIDs(l.meta.SessionID)
	if len(pids) != 1 {
		t.Fatalf("expected private daemon, got %v", pids)
	}
	var daemon int
	for pid := range pids {
		daemon = pid
		daemonEnv, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
		if err != nil || envValue(strings.Split(string(daemonEnv), "\x00"), "WAYLAND_DISPLAY") != l.meta.WaylandDisplay {
			t.Fatalf("browser daemon has the wrong display: %v", err)
		}
	}
	return daemon
}

// profile waits for the daemon's browser to run with the computer's
// private profile, and returns that profile.
func (l *agentBrowserLaunch) profile(t *testing.T) string {
	t.Helper()
	pids := browserDaemonPIDs(l.meta.SessionID)
	// The open command can return before Chromium's main process shows its
	// profile in /proc, so wait for it instead of reading one snapshot.
	profileArg := regexp.MustCompile(`--user-data-dir=([^ \x00]+)`)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for pid := range descendants(processes(), pids) {
			argv, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
			// Chromium can rewrite its process title as one space-separated argument.
			if strings.Contains(string(argv), "--type=") {
				continue
			}
			if match := profileArg.FindStringSubmatch(string(argv)); len(match) == 2 && match[1] == computerBrowserProfile(l.meta.SessionID) {
				return match[1]
			}
		}
	}
	t.Fatal("no browser with the computer's private profile")
	return ""
}

func TestAgentBrowserStaysOnSessionComputer(t *testing.T) {
	binary := agentBrowserTools(t)
	ctx, _ := longIdleDaemon.use(t)
	// Subscribe before the launch so a fast window event cannot be missed.
	events, err := NewClient("").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	launched := startAgentBrowser(t, ctx, binary, "browser-route", true)
	var daemon int
	if !t.Run("startup", func(t *testing.T) {
		daemon = launched.started(t)
	}) {
		return
	}
	if !t.Run("routing", func(t *testing.T) {
		launched.profile(t)
	}) {
		return
	}
	t.Run("window", func(t *testing.T) {
		ready, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		for found := false; !found; {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatal("browser app event stream closed")
				}
				if event.Status != nil && event.SessionID == launched.meta.SessionID {
					for _, app := range event.Status.Apps {
						found = found || strings.Contains(app.Title, event.SessionID)
					}
				}
			case <-ready.Done():
				t.Fatal("the browser window did not appear")
			}
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		if _, err := launched.client.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		waitProcessGone(t, daemon, "computer stop left its browser daemon alive")
	})
}

func TestAgentBrowserSessionNameStaysPrivate(t *testing.T) {
	binary := agentBrowserTools(t)
	ctx, _ := longIdleDaemon.use(t)
	// Start both browsers before waiting for either, with the same session name.
	first := startAgentBrowser(t, ctx, binary, "browser-first", false)
	second := startAgentBrowser(t, ctx, binary, "browser-second", false)
	firstDaemon, firstProfile := first.routing(t)
	secondDaemon, secondProfile := second.routing(t)
	if firstProfile == secondProfile {
		t.Fatal("computers shared a browser profile")
	}
	if firstDaemon == secondDaemon {
		t.Fatal("two computers reused one agent-browser daemon")
	}
	if _, err := first.client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	waitProcessGone(t, firstDaemon, "computer stop left its browser daemon alive")
	status, err := second.client.Status(ctx)
	if p, ok := processInfo(secondDaemon); err != nil || status.State != StateRunning || !ok || p.state == "Z" {
		t.Fatalf("stopping the first computer affected the second: %+v, %v", status, err)
	}
}

func waitProcessGone(t *testing.T, pid int, message string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if p, ok := processInfo(pid); !ok || p.state == "Z" {
			return
		}
	}
	t.Fatal(message)
}

// Set DYNA_COMPUTERS_PLAYWRIGHT_MODULE to an installed Playwright module to
// exercise SDK launches without downloading a browser or an npm dependency.
var playwrightModulePath = flag.String("playwright-module", "", "installed Playwright module for the live SDK test")

func TestPlaywrightStaysOnSessionComputer(t *testing.T) {
	module := *playwrightModulePath
	if module == "" {
		module = os.Getenv("DYNA_COMPUTERS_PLAYWRIGHT_MODULE")
	}
	if module == "" {
		t.Skip("set DYNA_COMPUTERS_PLAYWRIGHT_MODULE for the live SDK test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("live Playwright test needs node")
	}
	ctx, _ := startTestDaemon(t)
	client := NewClient(testSessionID(t, "playwright"))
	m, err := client.Register(ctx, "browser-test")
	if err != nil {
		t.Fatal(err)
	}
	env, err := Environment(m, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	executable := envValue(env, "DYNA_CHROMIUM_EXECUTABLE")
	if executable == "" {
		t.Skip("live Playwright test needs an installed Chrome binary")
	}
	script := `const {chromium}=require(process.argv[1]);
(async()=>{
 const browser=await chromium.launch({headless:false,executablePath:process.argv[3],args:['--ozone-platform=wayland','--no-sandbox','--disable-gpu','--disable-dev-shm-usage']});
 const page=await browser.newPage();
 await page.setContent('<title>'+process.argv[2]+'</title><h1>Playwright computer proof</h1>');
 process.stdin.resume();
 await new Promise(resolve=>process.stdin.on('end',resolve));
 await browser.close();
})().catch(error=>{console.error(error);process.exit(1)});`
	cmd := exec.CommandContext(ctx, node, "-e", script, module, m.SessionID, executable)
	cmd.Env = env
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		input.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Playwright: %v", err)
			}
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("Playwright did not close")
		}
	}()
	awaitStatus(t, ctx, client, func(s Status) bool {
		for _, app := range s.Apps {
			if strings.Contains(app.Title, m.SessionID) {
				return true
			}
		}
		return false
	})
	all := processes()
	root, ok := all[cmd.Process.Pid]
	if !ok {
		t.Fatal("Playwright driver exited")
	}
	for pid := range descendants(all, map[int]string{root.pid: root.start}) {
		argv, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if strings.Contains(string(argv), "--user-data-dir=") && strings.Contains(string(argv), computerBrowserProfile(m.SessionID)) {
			return
		}
	}
	t.Fatal("Playwright did not use the computer profile")
}

func TestOfflineDestroyStopsOnlyPrivateBrowserDaemon(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "offline-browser")
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(filepath.Join(Dir(id), "meta.json"), Meta{SessionID: id}, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, own := range []bool{false, true} {
		key := "FOO_AGENT_BROWSER_SOCKET_DIR"
		name := "owner"
		if own {
			key = "AGENT_BROWSER_SOCKET_DIR"
			name = "private"
		}
		env := mergeEnvironment(os.Environ(), map[string]string{key: root, "AGENT_BROWSER_DAEMON": "1"})
		child, err := startProcess(env, filepath.Join(Dir(id), name+".log"), "/bin/sleep", "600")
		if err != nil {
			t.Fatal(err)
		}
		defer child.stop()
		// Right after Start, /proc/<pid>/environ can still read as empty, and
		// cleanup would then not recognize the daemon. Wait until it is set.
		for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(time.Millisecond) {
			if env, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.cmd.Process.Pid), "environ")); len(env) > 0 {
				break
			} else if time.Now().After(deadline) {
				t.Fatal("daemon environment never became readable")
			}
		}
		if err := os.WriteFile(filepath.Join(root, name+".pid"), []byte(strconv.Itoa(child.cmd.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		if !own {
			defer func() {
				if !child.alive() {
					t.Error("offline cleanup killed an unrelated daemon")
				}
			}()
		} else {
			if err := destroyOffline(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			select {
			case <-child.done:
			// The exit event ends the wait; the limit only bounds a real hang.
			case <-time.After(8 * time.Second):
				t.Fatal("offline cleanup left the private daemon alive")
			}
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("private browser sockets survived offline cleanup")
	}
}

func TestStopBrowserDaemonsSignalsBeforeKill(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	id := testSessionID(t, "daemon-signal")
	root := browserSocketDir(os.Getenv("XDG_RUNTIME_DIR"), id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	// The fake browser records its SIGTERM. It carries no daemon marker, like
	// the computer processes whose environment strips it, so it is a plain
	// descendant of the daemon and not another daemon root.
	// It writes the ready file once its trap is set: a SIGTERM before that
	// kills the shell without a record. The stop signals the sleep helper and
	// the shell together; if the helper dies first, wait returns, so the shell
	// keeps looping until its own SIGTERM arrives instead of exiting early.
	marker := filepath.Join(t.TempDir(), "term")
	ready := filepath.Join(t.TempDir(), "ready")
	browser := filepath.Join(t.TempDir(), "fake-chromium")
	script := "#!/bin/sh\ntrap 'echo stopped > " + marker + "; exit 0' TERM\necho ready > " + ready + "\nsleep 600 & wait\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(browser, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := mergeEnvironment(os.Environ(), map[string]string{"AGENT_BROWSER_SOCKET_DIR": root, "AGENT_BROWSER_DAEMON": "1"})
	daemon, err := startProcess(env, filepath.Join(Dir(id), "daemon.log"), "/bin/sh", "-c", "env -u AGENT_BROWSER_DAEMON "+browser+" & wait")
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.stop()
	// Right after Start, /proc/<pid>/environ can still read as empty, and the
	// scan would then not recognize the daemon. Wait until it is set.
	for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(time.Millisecond) {
		if env, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(daemon.cmd.Process.Pid), "environ")); len(env) > 0 {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("daemon environment never became readable")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "daemon.pid"), []byte(strconv.Itoa(daemon.cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	// Wait for the daemon, its browser (trap set) and the browser's sleep
	// helper before the stop collects the process set.
	for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		_, notReady := os.Stat(ready)
		if roots := findBrowserDaemons(id); notReady == nil && len(roots) == 1 && len(descendants(processes(), roots)) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake browser never started")
		}
	}
	stopBrowserDaemons(id)
	b, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(b)) != "stopped" {
		t.Fatalf("browser stop killed without the graceful signal: %q, %v", b, err)
	}
}
