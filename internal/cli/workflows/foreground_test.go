package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/cli/version"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workflowsvc"
)

const workflowTestControl = "DYNA_WORKFLOW_TEST_CONTROL"

func foregroundFixture(t *testing.T, source string) string {
	t.Helper()
	inRepo(t)
	storetest.Isolate(t)
	serveWorkflowEngine(t)
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{
		Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
	}}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "workflow.js")
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return script
}

func runForegroundCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "dyna", SilenceUsage: true}
	root.AddCommand(NewRunCommand(), NewDemoCommand())
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func checkForegroundRun(t *testing.T, id, status string) runstore.Meta {
	t.Helper()
	meta, err := runstore.ReadMeta(id)
	if err != nil || meta.Pid == os.Getpid() || meta.Detached || meta.Status != status {
		t.Fatalf("foreground metadata = %+v, %v; CLI PID %d", meta, err, os.Getpid())
	}
	events, err := runstore.ReadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	starts, ends := 0, 0
	for _, event := range events {
		switch event.T {
		case "run_start":
			starts++
		case "run_end":
			ends++
			if event.Status != status {
				t.Fatalf("terminal event = %+v", event)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("run lifecycle = %d starts, %d ends", starts, ends)
	}
	messages, err := room.DefaultStore().Messages(meta.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.From == "run:"+id {
			t.Fatalf("foreground sent a completion post: %+v", message)
		}
	}
	return meta
}

func TestRunUsesRegisteredCoordinator(t *testing.T) {
	script := foregroundFixture(t, "return {value: args.value, cwd: process.cwd()}")
	dir := t.TempDir()
	stdout, stderr, err := runForegroundCLI(t, "run", script, "--json", "--args", `{"value":42}`, "--dir", dir, "--max-concurrent", "2", "--max-agents", "7")
	if err != nil {
		t.Fatalf("run = %v, stderr = %s", err, stderr)
	}
	var output struct {
		RunID, Status string
		Result        struct {
			Value int
			Cwd   string
		}
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil || output.Result.Value != 42 || output.Result.Cwd != dir || output.Status != "ok" {
		t.Fatalf("result = %+v, %v; stdout = %s", output, err, stdout)
	}
	checkForegroundRun(t, output.RunID, "ok")
}

func TestRunQuietPreservesResumeAndCacheSummary(t *testing.T) {
	script := foregroundFixture(t, `log("visible-log"); return 7`)
	stdout, stderr, err := runForegroundCLI(t, "run", script, "--json")
	if err != nil || !strings.Contains(stderr, "visible-log") {
		t.Fatalf("first run = %v, stderr = %s", err, stderr)
	}
	var first struct{ RunID string }
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runForegroundCLI(t, "run", script, "--quiet", "--resume", first.RunID)
	if err != nil || stdout != "7\n" || strings.Contains(stderr, "visible-log") {
		t.Fatalf("quiet run = %q, %q, %v", stdout, stderr, err)
	}
	if !strings.Contains(stderr, "run wf_") || !strings.Contains(stderr, "resuming from "+first.RunID) || !strings.Contains(stderr, "resume cache: 0/0") {
		t.Fatalf("quiet lost existing observer messages: %s", stderr)
	}
}

func TestRunStreamsResultsLargerThanALineScanner(t *testing.T) {
	script := foregroundFixture(t, "return Array.from({length:30000}, (_, i) => i)")
	stdout, stderr, err := runForegroundCLI(t, "run", script, "--quiet")
	var values []int
	if err != nil || len(stdout) <= 64*1024 || json.Unmarshal([]byte(stdout), &values) != nil || len(values) != 30000 || values[29999] != 29999 {
		t.Fatalf("large result = %d bytes, %d values, %v; stderr = %s", len(stdout), len(values), err, stderr)
	}
}

func TestRunPrintsChildDiagnosticOnce(t *testing.T) {
	script := foregroundFixture(t, "throw new Error('unique workflow failure')")
	stdout, stderr, err := runForegroundCLI(t, "run", script)
	if version.CommandExitCode(err) != 1 || err == nil || stdout != "" || strings.Count(stderr, "unique workflow failure") != 1 || strings.Contains(stderr, "exit status") {
		t.Fatalf("failed run = %q, %q, %v", stdout, stderr, err)
	}
	runs, readErr := runstore.List()
	if readErr != nil || len(runs) != 1 {
		t.Fatalf("failed runs = %+v, %v", runs, readErr)
	}
	checkForegroundRun(t, runs[0].ID, "error")
}

type cancelOnLog struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelOnLog) String() string { return w.buffer.String() }

func (w *cancelOnLog) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if strings.Contains(w.buffer.String(), "cancel-ready") {
		w.once.Do(w.cancel)
	}
	return n, err
}

func TestRunCancellationFinalizesBeforeReturning(t *testing.T) {
	script := foregroundFixture(t, `log("cancel-ready"); await sleep(100000000); return 1`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stderr := &cancelOnLog{cancel: cancel}
	cmd := NewRunCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{script})
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("canceled run succeeded")
	}
	runs, err := runstore.List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("canceled runs = %+v, %v", runs, err)
	}
	checkForegroundRun(t, runs[0].ID, "canceled")
	if strings.Count(stderr.String(), "context canceled") != 1 || strings.Contains(stderr.String(), "exit status") {
		t.Fatalf("cancellation diagnostics = %s", stderr.String())
	}
}

func TestDemoLeavesSavedProfilesUntouched(t *testing.T) {
	inRepo(t)
	storetest.Isolate(t)
	serveWorkflowEngine(t)
	owner := profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{
		Name: "owner-profile", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5, Default: true,
	}}}
	if err := owner.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(profile.DefaultPath(), old, old); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runForegroundCLI(t, "demo")
	if err != nil || !strings.Contains(stdout, "running demo workflow on mock workers...") || !strings.Contains(stdout, "open the desktop app") {
		t.Fatalf("demo = %q, %q, %v", stdout, stderr, err)
	}
	after, err := os.ReadFile(profile.DefaultPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("demo changed saved profiles: %v\n%s", err, after)
	}
	info, err := os.Stat(profile.DefaultPath())
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("demo rewrote the profiles file: %v, %v", info, err)
	}
	runs, err := runstore.List()
	if err != nil || len(runs) != 1 || runs[0].Name != "demo-fanout" || runs[0].Status != "ok" || runs[0].Detached {
		t.Fatalf("demo runs = %+v, %v", runs, err)
	}
}

func TestRunResultFormatterKeepsEmptyAndRawResults(t *testing.T) {
	for _, mode := range []string{"plain", "json", "raw"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := writeRunResult(cmd, workflowsvc.Result{ID: "wf_empty", Status: "ok"}, mode); err != nil {
				t.Fatal(err)
			}
			if mode == "json" {
				var output struct{ Result json.RawMessage }
				if json.Unmarshal(out.Bytes(), &output) != nil || string(output.Result) != "null" {
					t.Fatalf("empty JSON result = %s", out.String())
				}
			} else if out.String() != "\n" {
				t.Fatalf("empty result = %q", out.String())
			}
		})
	}
}

type slowWorkflowEvent struct {
	Kind, RunID string
	PID         int
}

func slowWorkflowWorker() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	conn, err := net.Dial("tcp", os.Getenv(workflowTestControl))
	if err != nil {
		return err
	}
	defer conn.Close()
	out := json.NewEncoder(conn)
	if err := out.Encode(slowWorkflowEvent{Kind: "ready", RunID: os.Getenv("DYNA_RUN_ID"), PID: os.Getpid()}); err != nil {
		return err
	}
	<-ctx.Done()
	if err := out.Encode(slowWorkflowEvent{Kind: "canceling"}); err != nil {
		return err
	}
	var finish string
	if err := json.NewDecoder(conn).Decode(&finish); err != nil {
		return err
	}
	if err := out.Encode(slowWorkflowEvent{Kind: "terminal"}); err != nil {
		return err
	}
	return ctx.Err()
}

func readSlowWorkflowEvent(t *testing.T, conn net.Conn, decoder *json.Decoder, kind string) slowWorkflowEvent {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event slowWorkflowEvent
	if err := decoder.Decode(&event); err != nil || event.Kind != kind {
		t.Fatalf("worker event = %+v, %v; want %s", event, err, kind)
	}
	return event
}

func TestRunSecondCtrlCPrintsRunIDAndLeavesCleanupRunning(t *testing.T) {
	script := foregroundFixture(t, "return 1")
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv(workflowTestControl, listener.Addr().String())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	parent := exec.Command(executable, "workflow-test-cli", "run", script)
	parent.Stdout, parent.Stderr = stdout, stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentEnded := make(chan struct{})
	var parentErr error
	go func() {
		parentErr = parent.Wait()
		close(parentEnded)
	}()
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		<-parentEnded
	})
	if err := listener.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	ready := readSlowWorkflowEvent(t, conn, decoder, "ready")
	if ready.RunID == "" {
		t.Fatalf("worker readiness = %+v", ready)
	}
	if err := parent.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	readSlowWorkflowEvent(t, conn, decoder, "canceling")
	if err := parent.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentEnded:
		if parentErr == nil {
			t.Fatal("second Ctrl-C exited successfully")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second Ctrl-C did not stop waiting")
	}
	printed, err := os.ReadFile(stderr.Name())
	if err != nil || !strings.Contains(string(printed), ready.RunID) || strings.Count(string(printed), "cleanup continues") != 1 || strings.Contains(string(printed), "signal: killed") {
		t.Fatalf("second interrupt diagnostic = %q, %v", printed, err)
	}
	// The live connection is the child's readiness barrier. It must still
	// accept the cleanup release after the real CLI process has exited.
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode("finish"); err != nil {
		t.Fatalf("second Ctrl-C killed the worker: %v", err)
	}
	readSlowWorkflowEvent(t, conn, decoder, "terminal")
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event slowWorkflowEvent
	if err := decoder.Decode(&event); !errors.Is(err, io.EOF) {
		t.Fatalf("worker did not exit after cleanup: %+v, %v", event, err)
	}
}

func TestRunResultFormatterReportsReferenceWriteFailure(t *testing.T) {
	storetest.Isolate(t)
	dir := runstore.RunDir("wf_reference-error")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parent-output"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(strings.Repeat("界", 1501))
	if err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	err = writeRunResult(cmd, workflowsvc.Result{ID: "wf_reference-error", Status: "ok", Value: string(value)}, "plain")
	if err == nil || !strings.Contains(err.Error(), "prepare parent result:") {
		t.Fatalf("formatter error = %v", err)
	}
}

// serveWorkflowEngine adds terminal calls to the existing ancestry fixture.
// The fixture uses local storage because its server models only call transport.
func serveWorkflowEngine(t *testing.T) {
	t.Helper()
	store, calls := enginetest.PlanAncestryCalls(t)
	lifecycle := workflowLifecycleCalls(t)
	for name, call := range lifecycle {
		calls[name] = call
	}
	enginetest.Serve(t, store.DataDir, calls)
}

func workflowLifecycleCalls(t *testing.T) map[string]websvc.Call {
	t.Helper()
	lifecycle, err := websvc.Bind([]websvc.Binding{
		{Name: "FinishRun", Access: websvc.Write, Scope: websvc.ScopeOperate,
			Method: func(_ context.Context, in callspec.RunFinish) (runstore.Meta, error) {
				if err := runstore.ForceStatus(in.Run, in.Status, in.Error); err != nil {
					return runstore.Meta{}, err
				}
				return runstore.ReadMeta(in.Run)
			}},
		{Name: "AddRunDirectories", Access: websvc.Write, Scope: websvc.ScopeOperate,
			Method: func(context.Context, callspec.RunDirectories) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
