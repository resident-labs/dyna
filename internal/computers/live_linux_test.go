//go:build linux

package computers

import (
	"context"
	"crypto/rand"
	"dyna-agent/internal/testsafe"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Each fixture needs its own process marker, even across concurrent test binaries.
// owned() intentionally finds matching adopted children outside the process tree.
func testSessionID(t *testing.T, prefix string) string {
	t.Helper()
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s-%x", prefix, token)
}

func TestMain(m *testing.M) {
	if len(os.Args) >= 4 && os.Args[1] == "computers" && os.Args[2] == "chromium" {
		args := os.Args[4:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if err := LaunchChromium(context.Background(), os.Args[3], args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if os.Getenv("DYNA_COMPUTER_TEST_RUN") == "" {
		os.Setenv("DYNA_COMPUTER_TEST_RUN", fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()))
	}
	if os.Getenv("DYNA_COMPUTER_TEST_HELPER") == "1" {
		// Every test daemon re-runs this binary. Only the parent checks for
		// leaks; a helper would count the other test daemon as its own leak.
		os.Exit(m.Run())
	}
	flag.Parse()
	cleanupCookieOwner, err := prepareCookieOwnerSeed()
	if err != nil {
		cleanupCookieOwner()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	before := helperPIDs()
	code := m.Run()
	for _, d := range []*sharedTestDaemon{defaultDaemon, longIdleDaemon} {
		if err := d.shutdown(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	for pid := range helperPIDs() {
		if !before[pid] {
			fmt.Fprintf(os.Stderr, "computer test daemon leaked: %d\n", pid)
			code = 1
		}
	}
	cleanupCookieOwner()
	os.Exit(code)
}
func helperPIDs() map[int]bool {
	out := map[int]bool{}
	token := os.Getenv("DYNA_COMPUTER_TEST_RUN")
	for pid, p := range processes() {
		if p.state == "Z" {
			continue
		}
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil || !strings.Contains(string(env), "DYNA_COMPUTER_TEST_RUN="+token+"\x00") {
			continue
		}
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if strings.Contains(string(b), "-test.run=^TestComputerDaemonHelper$") || strings.Contains(string(env), "DYNA_COMPUTER_TEST_HELPER=1\x00") && strings.Contains(string(env), "DYNA_COMPUTER_PROCESS=") {
			out[pid] = true
		}
	}
	return out
}

var computerTestMonitorEvery = flag.Duration("computer-test-monitor-every", 50*time.Millisecond, "app and idle poll interval for the private test daemon")
var computerTestIdleAfter = flag.Duration("computer-test-idle-after", 300*time.Millisecond, "idle timeout for the private test daemon")

// Live tests must not wait on the 15 s production start budget.
var computerTestStartOutput = flag.Duration("computer-test-start-output", 5*time.Second, "first output size budget for the test daemon")

func TestComputerDaemonHelper(t *testing.T) {
	if os.Getenv("DYNA_COMPUTER_TEST_HELPER") != "1" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	if err := RunDaemon(ctx, SupervisorOptions{IdleAfter: *computerTestIdleAfter, ViewGrace: 200 * time.Millisecond, StartOutputTimeout: *computerTestStartOutput, MonitorEvery: *computerTestMonitorEvery}); err != nil {
		t.Fatal(err)
	}
}

// A shared daemon starts once, the first time a test asks for it. A start
// costs seconds under -race, and most live tests only need a private
// computer. Tests that need their own daemon (killing it) use
// startTestDaemonWithExit.
type sharedTestDaemon struct {
	sync.Mutex
	args                  []string
	runtime, data, config string
	cmd                   *exec.Cmd
	done                  chan error
	stop                  chan struct{}
}

var (
	defaultDaemon = &sharedTestDaemon{}
	// An agent drives its browser over CDP, which the supervisor never sees,
	// and tests run no session turn, so this daemon must not idle-stop the
	// computers of tests that drive a browser.
	longIdleDaemon = &sharedTestDaemon{args: []string{"-computer-test-idle-after=30s"}}
)

func startTestDaemon(t *testing.T) (context.Context, *exec.Cmd) {
	t.Helper()
	return defaultDaemon.use(t)
}

func (d *sharedTestDaemon) use(t *testing.T) (context.Context, *exec.Cmd) {
	t.Helper()
	d.Lock()
	defer d.Unlock()
	if d.cmd == nil {
		if err := d.start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_RUNTIME_DIR", d.runtime)
	t.Setenv("XDG_DATA_HOME", d.data)
	t.Setenv("XDG_CONFIG_HOME", d.config)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(func() {
		cancel()
		d.reset(t)
	})
	return ctx, d.cmd
}

func (d *sharedTestDaemon) start() error {
	dirs := [3]string{}
	for i, pattern := range []string{"dc-", "dcd-", "dcc-"} {
		dir, err := testsafe.ShortTemp(pattern)
		if err != nil {
			return err
		}
		dirs[i] = dir
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(dirs[0], "daemon.log"))
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, append([]string{"-test.run=^TestComputerDaemonHelper$"}, d.args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{"DYNA_COMPUTER_TEST_HELPER": "1", "XDG_RUNTIME_DIR": dirs[0], "XDG_DATA_HOME": dirs[1], "XDG_CONFIG_HOME": dirs[2]})
	cmd.Stdout, cmd.Stderr = log, log
	stop := make(chan struct{})
	started := make(chan error, 1)
	// Pdeathsig fires when the thread that started the daemon exits, so one
	// goroutine keeps its thread until the daemon is stopped.
	go func() {
		goruntime.LockOSThread()
		started <- cmd.Start()
		<-stop
	}()
	if err := <-started; err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	socket := filepath.Join(dirs[0], "dyna", "computers", "control.sock")
	deadline := time.After(30 * time.Second)
	for {
		if conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond); err == nil {
			conn.Close()
			break
		}
		select {
		case err := <-done:
			b, _ := os.ReadFile(log.Name())
			return fmt.Errorf("daemon exited: %v: %s", err, b)
		case <-deadline:
			cmd.Process.Kill()
			return fmt.Errorf("daemon did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	d.runtime, d.data, d.config = dirs[0], dirs[1], dirs[2]
	d.cmd, d.done, d.stop = cmd, done, stop
	return nil
}

// resetSharedDaemon returns the shared daemon to a clean state after a test:
// every computer is destroyed, and the browser profile and settings are
// removed, so the next test starts from nothing.
func (d *sharedTestDaemon) reset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries, _ := os.ReadDir(Root())
	for _, entry := range entries {
		if err := Destroy(ctx, entry.Name()); err != nil {
			t.Errorf("destroy %s: %v", entry.Name(), err)
			continue
		}
		// Destroy preserves real session metadata. This private fixture has no
		// history to keep; later tests must not destroy these computers again.
		if err := os.RemoveAll(Dir(entry.Name())); err != nil {
			t.Errorf("remove computer %s: %v", entry.Name(), err)
		}
	}
	// The browser master profile lives next to the computers directory.
	if err := os.RemoveAll(filepath.Dir(browserMaster())); err != nil {
		t.Errorf("remove browser profile: %v", err)
	}
	configs, _ := os.ReadDir(d.config)
	for _, entry := range configs {
		if err := os.RemoveAll(filepath.Join(d.config, entry.Name())); err != nil {
			t.Errorf("remove config %s: %v", entry.Name(), err)
		}
	}
}

func (d *sharedTestDaemon) shutdown() error {
	d.Lock()
	defer d.Unlock()
	if d.cmd == nil {
		return nil
	}
	defer func() {
		close(d.stop)
		os.RemoveAll(d.runtime)
		os.RemoveAll(d.data)
		os.RemoveAll(d.config)
	}()
	d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-d.done:
		if err != nil {
			b, _ := os.ReadFile(filepath.Join(d.runtime, "daemon.log"))
			return fmt.Errorf("shared daemon: %v: %s", err, b)
		}
	case <-time.After(8 * time.Second):
		d.cmd.Process.Kill()
		<-d.done
		return fmt.Errorf("shared daemon failed to stop")
	}
	return nil
}

// awaitStatus re-checks the status on every daemon event, so a slow host (for
// example one that is late to show a Chromium window) only makes the test
// longer. The 30 s limit only bounds a condition that never becomes true.
func awaitStatus(t *testing.T, ctx context.Context, c *Client, test func(Status) bool) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var last Status
	for {
		if s, err := c.Status(ctx); err == nil {
			last = s
			if test(s) {
				return s
			}
		}
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case <-ctx.Done():
			t.Fatalf("state did not converge: %+v", last)
		}
	}
}
func TestComputerDaemonRegistration(t *testing.T) {
	ctx, _ := startTestDaemon(t)
	client := NewClient(testSessionID(t, "registered"))
	m, err := client.Register(ctx, "room")
	if err != nil {
		t.Fatal(err)
	}
	if m.Display == "" || m.WaylandDisplay == "" {
		t.Fatal(m)
	}
	s, err := client.Status(ctx)
	if err != nil || s.State != StateCreated || s.RSSKiB != 0 {
		t.Fatal(s, err)
	}
	shot, err := client.Shot(ctx, true)
	if err != nil || shot.Message != "no windows yet" || shot.Path != "" {
		t.Fatal(shot, err)
	}
	a, err := client.Act(ctx, parseActions(t, `[{"type":"screenshot"}]`), time.Second, "")
	if err != nil || a.Message != "no windows yet" {
		t.Fatal(a, err)
	}
	if err = Destroy(ctx, m.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), m.WaylandDisplay)); !os.IsNotExist(err) {
		t.Fatal("public Wayland socket survived destroy")
	}
	if s := savedStatus(m.SessionID); s.State != StateDestroyed {
		t.Fatal(s)
	}
}

// The shared daemon has the locale of the first test that started it, so a
// reset must leave it a clean state: no computers, no browser profile, no
// saved settings.
func TestSharedDaemonResetRemovesState(t *testing.T) {
	ctx, _ := startTestDaemon(t)
	id := testSessionID(t, "reset-state")
	if _, err := NewClient(id).Register(ctx, "reset-proof"); err != nil {
		t.Fatal(err)
	}
	master := browserMaster()
	settingsFile := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dyna", "settings.json")
	for _, path := range []string{filepath.Join(master, "Default", "Cookies"), settingsFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaultDaemon.reset(t)
	for _, path := range []string{Dir(id), master, settingsFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived the reset: %v", path, err)
		}
	}
}
