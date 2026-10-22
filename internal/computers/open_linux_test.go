//go:build linux

package computers

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenAppReportsExitAndLogWithoutAWindow(t *testing.T) {
	base, _ := startTestDaemon(t)
	ctx, cancel := context.WithTimeout(base, 3*time.Second)
	defer cancel()
	client := NewClient(testSessionID(t, "open-exit"))
	if _, err := client.Register(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	var opened Opened
	err := client.Call(ctx, Request{Op: "open", Status: "Checking launch failure", Command: []string{"sh", "-c", "printf launch-failed >&2; exit 7"}, TimeoutMS: 2000}, &opened)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "log:") {
		t.Fatalf("failure: %v", err)
	}
	start := strings.Index(err.Error(), "(log: ")
	path := strings.TrimSuffix(err.Error()[start+6:], ")")
	log, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(log), "launch-failed") {
		t.Fatalf("launch log: %q %v", log, readErr)
	}
}

func TestOpenAppTimesOutWithoutAWindow(t *testing.T) {
	base, _ := startTestDaemon(t)
	ctx, cancel := context.WithTimeout(base, 3*time.Second)
	defer cancel()
	client := NewClient(testSessionID(t, "open-timeout"))
	if _, err := client.Register(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	var opened Opened
	err := client.Call(ctx, Request{Op: "open", Status: "Checking launch timeout", Command: []string{"sh", "-c", "exec sleep 30"}, TimeoutMS: 20}, &opened)
	if err == nil || !strings.Contains(err.Error(), "no new window") || !strings.Contains(err.Error(), "log:") {
		t.Fatalf("timeout: %v", err)
	}
}

// The event stream ends with the open request's context. When the timeout
// and the closed stream are ready together, select may take either, so a
// timed-out open must report the timeout either way.
func TestClosedEventsAfterTimeoutReportNoWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := eventsClosedError(ctx, "app", "x.log"); !strings.Contains(err.Error(), "no new window") || !strings.Contains(err.Error(), "log: x.log") {
		t.Fatalf("closed stream after timeout: %v", err)
	}
	if err := eventsClosedError(context.Background(), "app", "x.log"); !strings.Contains(err.Error(), "event connection closed") {
		t.Fatalf("closed stream before timeout: %v", err)
	}
}

// No maintenance loop runs here. The status read must publish the first app
// observation, otherwise an open request waiting on events can miss it forever.
func TestStatusReadPublishesNewWindowBeforeMaintenance(t *testing.T) {
	for _, name := range []string{"labwc", "foot", "wlr-randr"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("live app test needs %s", name)
		}
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runtime, err := os.MkdirTemp("", "open-status-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtime)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	events := make(chan Event, 10)
	s := &supervisor{ctx: ctx, meta: Meta{SessionID: testSessionID(t, "status-window")}, state: Status{Width: 1280, Height: 800}, apps: map[int]string{}, emit: func(event Event) { events <- event }}
	if err := os.MkdirAll(RuntimeDir(s.meta.SessionID), 0700); err != nil {
		t.Fatal(err)
	}
	desktop, err := s.startDesktop(ctx, 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.process.stop()
	s.compositor = desktop.process
	s.privateWayland, s.privateDisplay = desktop.wayland, desktop.display
	p, err := startProcess(s.desktopEnv(), filepath.Join(Dir(s.meta.SessionID), "terminal.log"), "foot", "--config=/dev/null", "--title=Status first", "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	for {
		state := s.status()
		if len(state.Apps) > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	select {
	case event := <-events:
		if event.Type != "apps" || len(event.Status.Apps) != 1 || event.Status.Apps[0].Title != "Status first" {
			t.Fatalf("event: %+v", event)
		}
	default:
		t.Fatal("status observed the window without publishing an apps event")
	}
	s.readApps()
	select {
	case event := <-events:
		t.Fatalf("unchanged apps emitted twice: %+v", event)
	default:
	}
}

func TestOpenTimeoutStopsChildAfterLauncherExits(t *testing.T) {
	launcher, logPath := startTimeoutLauncher(t, "sh", "-c", "sleep 30 & echo $!; exit 0")
	// The exit event guarantees that the launcher reported its child PID.
	// A loaded host cannot expire the deadline before that report.
	<-launcher.done
	if launcher.err != nil {
		t.Fatal(launcher.err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(log)))
	if err != nil {
		t.Fatalf("child PID: %q %v", log, err)
	}
	assertOpenTimeoutKillsChild(t, launcher, pid, logPath)
}

func TestOpenTimeoutStopsChildBeforeLauncherReportsPID(t *testing.T) {
	dir := t.TempDir()
	reportPath, gatePath := filepath.Join(dir, "report"), filepath.Join(dir, "gate")
	for _, path := range []string{reportPath, gatePath} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := os.OpenFile(reportPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { report.Close() })
	// The child is reported on a separate pipe. The launcher then waits
	// on the gate before writing the PID to its normal output.
	launcher, logPath := startTimeoutLauncher(t, "sh", "-c", "sleep 30 & child=$!; printf '%s\\n' \"$child\" > \"$1\"; read release < \"$2\"; echo \"$child\"; exit 0", "launcher", reportPath, gatePath)
	type reportResult struct {
		line string
		err  error
	}
	reported := make(chan reportResult, 1)
	go func() {
		line, err := bufio.NewReader(report).ReadString('\n')
		reported <- reportResult{line, err}
	}()
	var result reportResult
	select {
	case result = <-reported:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-launcher.done:
		t.Fatalf("launcher exited before reporting its child: %v", launcher.err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	pid, err := strconv.Atoi(strings.TrimSpace(result.line))
	if err != nil {
		t.Fatal(err)
	}
	assertOpenTimeoutKillsChild(t, launcher, pid, logPath)
	if log, err := os.ReadFile(logPath); err != nil || len(log) != 0 {
		t.Fatalf("launcher output before timeout: %q %v", log, err)
	}
}

func startTimeoutLauncher(t *testing.T, command ...string) (*process, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "launcher.log")
	launcher, err := startProcess(os.Environ(), logPath, command...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Kill(-launcher.cmd.Process.Pid, unix.SIGKILL)
		<-launcher.done
	})
	return launcher, logPath
}

func assertOpenTimeoutKillsChild(t *testing.T, launcher *process, pid int, logPath string) {
	t.Helper()
	defer unix.Kill(pid, unix.SIGKILL)
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)

	ctx, cancel := context.WithDeadline(t.Context(), time.Now())
	defer cancel()
	if _, err := waitForApp(ctx, launcher, nil, nil, logPath); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no new window") {
		t.Fatalf("timeout: %v", err)
	}
	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(polls, 500); err != nil || n == 0 {
		t.Fatalf("launcher child %d survived timeout: %v", pid, err)
	}
}
