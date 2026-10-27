package workflowsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

const launchTestControl = "DYNA_LAUNCH_TEST_CONTROL"
const launchTestMode = "DYNA_LAUNCH_TEST_MODE"

type launchEvent struct {
	Kind       string
	PID        int
	Process    callerid.Process
	Actor      callerid.Identity
	Options    Options
	Cwd        string
	Argv       []string
	Detached   string
	Room       string
	Session    string
	LockMarker string
	LockTarget string
}

// TestMain makes this test executable a real gated worker or its real parent.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "run-worker":
			callerid.WaitForLaunch()
			if err := launchWorkerFixture(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(7)
			}
			os.Exit(0)
		case "launch-test-parent":
			_, err := Launch(context.Background(), Options{ScriptSrc: "return 1"}, LaunchOptions{Foreground: &ForegroundOptions{Stdout: os.Stdout, Stderr: os.Stderr}})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func launchWorkerFixture() error {
	if marker := os.Getenv("DYNA_LAUNCH_TEST_ACTION"); marker != "" {
		if err := os.WriteFile(marker, nil, 0600); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	conn, err := net.Dial("tcp", os.Getenv(launchTestControl))
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	var opts Options
	if err := json.Unmarshal(data, &opts); err != nil {
		return err
	}
	records, err := callerid.OpenStore(store.DataDir())
	if err != nil {
		return err
	}
	resolver, err := callerid.New("/proc", 0, records)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Read the actual launch environment: mode.init intentionally clears the
	// inherited stamp before a Go test binary reaches TestMain.
	initial, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		return err
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(string(initial), "\x00") {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	lockTarget, _ := os.Readlink("/proc/self/fd/" + os.Getenv("DYNA_LAUNCH_TEST_LOCK_FD"))
	self, err := callerid.ProcessOf(os.Getpid())
	if err != nil {
		return err
	}
	event := launchEvent{Kind: "ready", PID: os.Getpid(), Process: self, Actor: resolver.Resolve(os.Getpid()), Options: opts, Cwd: cwd,
		Argv: os.Args[3:], Detached: env["DYNA_RUN_DETACHED"], Room: env[mode.EnvRoomID], Session: env[mode.EnvSession],
		LockMarker: os.Getenv("DYNA_SUBAGENT_LOCK_FD"), LockTarget: lockTarget}
	out := json.NewEncoder(conn)
	if err := out.Encode(event); err != nil {
		return err
	}
	if os.Getenv(launchTestMode) == "inspect" {
		fmt.Fprint(os.Stdout, "result bytes\n")
		fmt.Fprint(os.Stderr, "progress bytes\n")
		return nil
	}
	<-ctx.Done()
	if err := out.Encode(launchEvent{Kind: "canceling"}); err != nil {
		return err
	}
	if os.Getenv(launchTestMode) == "slow" {
		var finish string
		if err := json.NewDecoder(conn).Decode(&finish); err != nil {
			return err
		}
	}
	if err := out.Encode(launchEvent{Kind: "terminal"}); err != nil {
		return err
	}
	return ctx.Err()
}

func launchFixture(t *testing.T, workerMode string) *net.TCPListener {
	t.Helper()
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { _ = database.Close(dataDir) })
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv(launchTestControl, listener.Addr().String())
	t.Setenv(launchTestMode, workerMode)
	return listener
}

func acceptWorker(t *testing.T, listener *net.TCPListener) (net.Conn, *json.Decoder, launchEvent) {
	t.Helper()
	if err := listener.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	decoder := json.NewDecoder(conn)
	event := readLaunchEvent(t, conn, decoder, "ready")
	process, err := os.FindProcess(event.PID)
	if err == nil {
		t.Cleanup(func() {
			defer process.Release()
			// A failed assertion must not leave its fixture running.
			if current, err := callerid.ProcessOf(event.PID); err == nil && current == event.Process {
				_ = process.Kill()
			}
		})
	}
	return conn, decoder, event
}

func readLaunchEvent(t *testing.T, conn net.Conn, decoder *json.Decoder, kind string) launchEvent {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event launchEvent
	if err := decoder.Decode(&event); err != nil || event.Kind != kind {
		t.Fatalf("worker event = %+v, %v; want %s", event, err, kind)
	}
	return event
}

func releaseLaunchWorker(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode("finish"); err != nil {
		t.Fatal(err)
	}
}

func expectLaunchEOF(t *testing.T, conn net.Conn, decoder *json.Decoder) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event launchEvent
	if err := decoder.Decode(&event); !errors.Is(err, io.EOF) {
		t.Fatalf("worker did not exit after cleanup: %+v, %v", event, err)
	}
}

type launchOutcome struct {
	id  string
	err error
}

func TestForegroundLaunchRegistersAndPreservesInputsAndStreams(t *testing.T) {
	listener := launchFixture(t, "inspect")
	t.Setenv("DYNA_RUN_DETACHED", "1")
	t.Setenv("DYNA_RUN_ID", "wf_parent")
	t.Setenv(mode.EnvRoomID, "room_test")
	t.Setenv(mode.EnvSession, "ses_parent")
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	t.Setenv("DYNA_SUBAGENT_LOCK_FD", strconv.Itoa(int(lock.Fd())))
	t.Setenv("DYNA_LAUNCH_TEST_LOCK_FD", strconv.Itoa(int(lock.Fd())))
	opts := Options{ScriptSrc: "return args", ScriptPath: "/source/original.js", Name: "preserved", Args: map[string]any{"nested": []any{float64(3), "value"}},
		WorkDir: t.TempDir(), WorkflowDir: "/workflows", ResumeFrom: "wf_resume", MaxConc: 3, MaxAgents: 9}
	var stdout, stderr bytes.Buffer
	ended := make(chan launchOutcome, 1)
	go func() {
		id, err := Launch(t.Context(), opts, LaunchOptions{Foreground: &ForegroundOptions{Stdout: &stdout, Stderr: &stderr, Output: "json", Quiet: true}})
		ended <- launchOutcome{id, err}
	}()
	_, _, event := acceptWorker(t, listener)
	result := <-ended
	if result.err != nil || result.id == "wf_parent" || result.id == "" {
		t.Fatalf("launch = %+v", result)
	}
	if event.Actor.Kind != callerid.Worker || event.Actor.Run != result.id || event.Actor.Agent != 0 || event.Actor.Session != "" {
		t.Fatalf("child identity = %+v", event.Actor)
	}
	cwd, _ := os.Getwd()
	if !reflect.DeepEqual(event.Options, opts) || event.Cwd != cwd || event.Detached != "0" || event.Room != "room_test" || event.Session != "ses_parent" {
		t.Fatalf("child inputs = %+v; want %+v, cwd %s", event, opts, cwd)
	}
	if !slices.Equal(event.Argv, []string{"--output", "json", "--quiet"}) || event.LockMarker != "" || event.LockTarget == lock.Name() {
		t.Fatalf("child flags/locks = %+v", event)
	}
	if stdout.String() != "result bytes\n" || stderr.String() != "progress bytes\n" {
		t.Fatalf("streams = %q, %q", stdout.String(), stderr.String())
	}
	parent, err := callerid.ProcessOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	records, err := callerid.OpenStore(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if actor, found, err := records.ProcessIdentity(parent); err != nil || found {
		t.Fatalf("parent gained a launch record: %+v, %v, %v", actor, found, err)
	}
}

func TestForegroundCancellationJoinsCleanup(t *testing.T) {
	listener := launchFixture(t, "slow")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ended := make(chan launchOutcome, 1)
	go func() {
		id, err := Launch(ctx, Options{ScriptSrc: "return 1"}, LaunchOptions{Foreground: &ForegroundOptions{}})
		ended <- launchOutcome{id, err}
	}()
	conn, decoder, _ := acceptWorker(t, listener)
	cancel()
	readLaunchEvent(t, conn, decoder, "canceling")
	select {
	case result := <-ended:
		t.Fatalf("launch returned before cleanup: %+v", result)
	default:
	}
	releaseLaunchWorker(t, conn)
	readLaunchEvent(t, conn, decoder, "terminal")
	result := <-ended
	var failed *exec.ExitError
	if result.id == "" || !errors.As(result.err, &failed) || failed.ExitCode() != 7 {
		t.Fatalf("canceled launch = %+v", result)
	}
}

func TestForegroundSecondInterruptLeavesCleanupRunning(t *testing.T) {
	listener := launchFixture(t, "slow")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopWaiting := make(chan struct{})
	ended := make(chan launchOutcome, 1)
	go func() {
		id, err := Launch(ctx, Options{ScriptSrc: "return 1"}, LaunchOptions{Foreground: &ForegroundOptions{StopWaiting: stopWaiting}})
		ended <- launchOutcome{id, err}
	}()
	conn, decoder, ready := acceptWorker(t, listener)
	cancel()
	readLaunchEvent(t, conn, decoder, "canceling")
	close(stopWaiting)
	result := <-ended
	if result.id == "" || !errors.Is(result.err, ErrWaitInterrupted) {
		t.Fatalf("interrupted launch = %+v", result)
	}
	if _, err := callerid.ProcessOf(ready.PID); err != nil {
		t.Fatalf("second interrupt killed the coordinator: %v", err)
	}
	releaseLaunchWorker(t, conn)
	readLaunchEvent(t, conn, decoder, "terminal")
	expectLaunchEOF(t, conn, decoder)
}

func TestForegroundCompletionWinsCancellationRace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ended := make(chan error, 1)
	ended <- nil
	if err := waitForeground(ctx, &exec.Cmd{}, ended, nil); err != nil {
		t.Fatalf("completed result lost to cancellation: %v", err)
	}
}

func TestForegroundLaunchCanceledBeforeStart(t *testing.T) {
	storetest.Isolate(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	id, err := Launch(ctx, Options{}, LaunchOptions{Executable: "/must-not-start", Foreground: &ForegroundOptions{}})
	if id != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch = %q, %v", id, err)
	}
	if _, err := os.Stat(runstore.RunsDir()); !os.IsNotExist(err) {
		t.Fatalf("canceled launch staged a run: %v", err)
	}
}

func TestForegroundRegistrationFailureKeepsWorkerGated(t *testing.T) {
	storetest.Isolate(t)
	if err := os.MkdirAll(filepath.Join(store.DataDir(), "dyna.db"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "worker-action")
	t.Setenv("DYNA_LAUNCH_TEST_ACTION", marker)
	id, err := Launch(t.Context(), Options{ScriptSrc: "return 1"}, LaunchOptions{Foreground: &ForegroundOptions{}})
	if id != "" || err == nil {
		t.Fatalf("unregistered launch = %q, %v", id, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unregistered worker ran: %v", err)
	}
}

func TestLaunchThatCannotStartRecordsAFailedRun(t *testing.T) {
	storetest.Isolate(t)
	env := []string{mode.EnvMode + "=" + mode.Subagent, mode.EnvSession + "=ses_20261026-000000-abcdef01", mode.EnvRoomID + "=room_x", mode.EnvRoom + "=x"}
	_, err := Launch(t.Context(), Options{ScriptSrc: "return 1", Name: "gone"}, LaunchOptions{Executable: filepath.Join(t.TempDir(), "missing-dyna"), Env: env})
	if err == nil {
		t.Fatal("launch of a missing executable succeeded")
	}
	runs, listErr := runstore.List()
	if listErr != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, %v", runs, listErr)
	}
	got := runs[0]
	if got.Status != "error" || got.ParentSession != "ses_20261026-000000-abcdef01" || got.RoomID != "room_x" || !strings.Contains(got.Error, "worker did not start") {
		t.Fatalf("failed run meta = %+v", got)
	}
}

func TestLaunchExecutableDefaultsToTheInstalledPath(t *testing.T) {
	got, err := launchExecutable("")
	if err != nil || strings.HasPrefix(got, "/proc/") || filepath.Base(got) != filepath.Base(os.Args[0]) {
		t.Fatalf("default executable = %q, %v", got, err)
	}
	if got, err := launchExecutable("/given"); err != nil || got != "/given" {
		t.Fatalf("given executable = %q, %v", got, err)
	}
}

func TestDetachedLaunchStillReturnsBeforeWorkerExit(t *testing.T) {
	listener := launchFixture(t, "slow")
	id, err := Launch(t.Context(), Options{ScriptSrc: "return 1"}, LaunchOptions{})
	if err != nil || id == "" {
		t.Fatalf("detached launch = %q, %v", id, err)
	}
	conn, decoder, ready := acceptWorker(t, listener)
	if ready.Detached != "1" || len(ready.Argv) != 0 || ready.Actor.Run != id || ready.Actor.Kind != callerid.Worker {
		t.Fatalf("detached child = %+v", ready)
	}
	if _, err := os.Stat(filepath.Join(runstore.RunDir(id), "daemon.log")); err != nil {
		t.Fatalf("detached launch lost daemon log: %v", err)
	}
	process, err := os.FindProcess(ready.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	readLaunchEvent(t, conn, decoder, "canceling")
	releaseLaunchWorker(t, conn)
	readLaunchEvent(t, conn, decoder, "terminal")
	expectLaunchEOF(t, conn, decoder)
}
