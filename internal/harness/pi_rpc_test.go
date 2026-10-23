package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store/storetest"
)

// The fake pi speaks the RPC protocol by re-running this test binary, so the
// assertions see real JSON records, not shell-approximated ones.
const (
	envFakePi       = "DYNA_TEST_FAKE_PI"
	envFakePiLog    = "DYNA_TEST_FAKE_PI_LOG"
	envFakePiHold   = "DYNA_TEST_FAKE_PI_HOLD"
	envFakePiGate   = "DYNA_TEST_FAKE_PI_GATE"
	envFakePiSignal = "DYNA_TEST_FAKE_PI_SIGNAL"
)

// fakePiMain runs when the test binary is executed as `pi`. Every mode keeps
// the process alive after a turn ends, like a real pi --mode rpc process:
// a held prompt answers no message_end until steering or abort arrives.
func fakePiMain() {
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(os.Getenv(envFakePiLog), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, format+"\n", args...)
		_ = f.Close()
	}
	logf("SPAWN %d %s", os.Getpid(), strings.Join(os.Args[1:], " "))
	out := bufio.NewWriter(os.Stdout)
	emit := func(record map[string]any) {
		b, err := json.Marshal(record)
		if err != nil {
			return
		}
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	respond := func(id, command string, data map[string]any) {
		record := map[string]any{"id": id, "type": "response", "command": command, "success": true}
		if data != nil {
			record["data"] = data
		}
		emit(record)
	}
	finish := func(prompt string) {
		emit(map[string]any{"type": "message_end", "message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "done: " + prompt}},
			"usage":   map[string]any{"input": 10, "output": 5, "cost": map[string]any{"total": 0.01}},
		}})
		emit(map[string]any{"type": "agent_settled"})
	}
	in := bufio.NewReader(os.Stdin)
	hold := os.Getenv(envFakePiHold) != ""
	gate := os.Getenv(envFakePiGate) != ""
	// signal dials the test's listener the moment a prompt arrives, so the
	// test cancels on a real event instead of polling the log.
	signal := func() {
		path := os.Getenv(envFakePiSignal)
		if path == "" {
			return
		}
		if conn, err := net.Dial("unix", path); err == nil {
			_, _ = conn.Write([]byte("prompted\n"))
			_ = conn.Close()
		}
	}
	// gatedPrompt holds a prompt whose response waits for the abort, the
	// exact window where a canceled turn must still send the abort.
	gatedPrompt := ""
	for {
		line, err := in.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			if err != nil {
				return
			}
			continue
		}
		var command struct {
			ID                string `json:"id"`
			Type              string `json:"type"`
			Message           string `json:"message"`
			StreamingBehavior string `json:"streamingBehavior"`
		}
		if json.Unmarshal([]byte(line), &command) != nil {
			if err != nil {
				return
			}
			continue
		}
		switch command.Type {
		case "prompt":
			if command.StreamingBehavior != "" {
				logf("STEER %s", command.Message)
				respond(command.ID, "prompt", map[string]any{"disposition": "queued"})
				if hold {
					finish(command.Message)
				}
			} else {
				logf("PROMPT %s", command.Message)
				if gate {
					gatedPrompt = command.ID
					signal()
				} else {
					respond(command.ID, "prompt", map[string]any{"disposition": "started"})
					if !hold {
						finish(command.Message)
					}
				}
			}
		case "abort":
			logf("ABORT")
			respond(command.ID, "abort", nil)
			if gatedPrompt != "" {
				// Pi can acknowledge the abort while the prompt's preflight
				// is still pending, then start that run: acknowledge first,
				// start the run second. The run events land on a process
				// supervision must already have stopped.
				gated := gatedPrompt
				gatedPrompt = ""
				signal()
				finish("unwatched " + gated)
			} else {
				emit(map[string]any{"type": "agent_settled", "aborted": true})
			}
		case "compact":
			logf("COMPACT")
			respond(command.ID, "compact", map[string]any{"summary": "ok"})
		}
		if err != nil {
			return
		}
	}
}

// installFakePi puts a `pi` on PATH that re-runs the test binary in fake
// mode. hold keeps a plain prompt unsettled until steering or abort arrives.
func installFakePi(t *testing.T, hold bool) (logPath string) {
	t.Helper()
	storetest.Isolate(t)
	// The fake and its process guard re-execute the test binary. Keep race
	// checks without their one-second child exit grace.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	t.Cleanup(sessionhost.ResetForTest)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "pi.log")
	script := "#!/bin/sh\nexec " + shellquote.Quote(exe) + " \"$@\"\n"
	if err := fsutil.WriteExecutable(filepath.Join(dir, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakePiProfile(logPath string, hold bool) profile.Profile {
	env := map[string]string{envFakePi: "1", envFakePiLog: logPath}
	if hold {
		env[envFakePiHold] = "1"
	}
	return profile.Profile{Name: "p", Harness: profile.HarnessPi, Env: env}
}

// armedSource arms one steering message before the turn starts and keeps
// offering it until the supervisor takes it.
type armedSource struct {
	mu           sync.Mutex
	armed, taken bool
	message      string
	activ        bool
	deactiv      bool
}

func (s *armedSource) Activate() error {
	s.mu.Lock()
	s.activ = true
	s.mu.Unlock()
	return nil
}

func (s *armedSource) Read(offset int64, final bool) ([]runstore.SteeringMessage, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed || s.taken {
		return nil, offset, nil
	}
	s.taken = true
	return []runstore.SteeringMessage{{Message: s.message}}, offset + 1, nil
}

func (s *armedSource) Deactivate() {
	s.mu.Lock()
	s.deactiv = true
	s.mu.Unlock()
}

// TestPiSessionTurnsReuseOneRPCProcess runs two turns of one pi session and
// expects one long-lived pi --mode rpc process, not one process per turn.
func TestPiSessionTurnsReuseOneRPCProcess(t *testing.T) {
	logPath := installFakePi(t, false)
	p := fakePiProfile(logPath, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workDir := t.TempDir()
	var sessionID string
	r1, err := RunWithOptions(ctx, p, "task one", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{
		OnSession: func(id string) { sessionID = id },
	}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if r1.Output != "done: task one" {
		t.Fatalf("first output = %q", r1.Output)
	}
	if sessionID == "" {
		t.Fatal("first turn reported no session id")
	}
	if r1.Usage.InputTokens != 10 {
		t.Fatalf("first usage = %#v", r1.Usage)
	}
	r2, err := RunWithOptions(ctx, p, "task two", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{ResumeID: sessionID}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if r2.Output != "done: task two" {
		t.Fatalf("second output = %q", r2.Output)
	}
	log := readTestFile(t, logPath)
	if got := strings.Count(log, "SPAWN "); got != 1 {
		t.Fatalf("second turn spawned a new pi process; SPAWN count = %d:\n%s", got, log)
	}
	if !strings.Contains(log, "--mode rpc") || strings.Contains(log, "--print") {
		t.Fatalf("pi did not run in rpc mode:\n%s", log)
	}
	if !strings.Contains(log, "PROMPT task two") {
		t.Fatalf("second prompt never reached the session process:\n%s", log)
	}
}

// TestPiSteeringQueuesOverRPCWithoutInterrupt delivers mid-run steering as an
// RPC steer-queued prompt: the held run continues and answers, without
// stopping or restarting the pi process.
func TestPiSteeringQueuesOverRPCWithoutInterrupt(t *testing.T) {
	logPath := installFakePi(t, true)
	p := fakePiProfile(logPath, true)
	source := &armedSource{armed: true, message: "check the tests too"}
	var delivered []runstore.SteeringMessage
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := RunWithOptions(ctx, p, "first task", t.TempDir(), false, JournalOptions{}, SteeringOptions{
		Source:    source,
		OnMessage: func(msg runstore.SteeringMessage, _ *int64) { delivered = append(delivered, msg) },
	}, SessionOptions{}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	// The held run answers only after the steer, so the final text proves the
	// same run continued past the queued steering.
	if !strings.HasPrefix(r.Output, "done: [DYNA STEERING]") {
		t.Fatalf("output = %q", r.Output)
	}
	log := readTestFile(t, logPath)
	if !strings.Contains(log, "STEER [DYNA STEERING]") || !strings.Contains(log, "check the tests too") {
		t.Fatalf("steering never reached pi as an RPC steer:\n%s", log)
	}
	if got := strings.Count(log, "SPAWN "); got != 1 {
		t.Fatalf("steering restarted pi; SPAWN count = %d:\n%s", got, log)
	}
	if len(delivered) != 1 || delivered[0].Message != "check the tests too" {
		t.Fatalf("OnMessage deliveries = %#v", delivered)
	}
}

// fakePiPID reads the pid the fake logged at startup.
func fakePiPID(t *testing.T, log string) int {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "SPAWN" {
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("fake pi SPAWN line has no pid: %s", line)
			}
			return pid
		}
	}
	t.Fatal("fake pi log has no SPAWN line")
	return 0
}

// TestPiCancelAbortsTheRPCRun cancels a turn while pi holds the prompt
// response and proves the RPC abort still reaches pi. The cancel can land
// after the prompt was written but before its response arrived; that window
// must not leave a run unwatched. The fake gates its prompt response on the
// abort itself, so the window is deterministic, and signals the listener the
// moment the prompt arrives, so the test cancels on an event, not a poll.
func TestPiCancelAbortsTheRPCRun(t *testing.T) {
	logPath := installFakePi(t, false)
	signalPath := filepath.Join(t.TempDir(), "prompted.sock")
	listener, err := net.Listen("unix", signalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	p := fakePiProfile(logPath, false)
	p.Env[envFakePiGate] = "1"
	p.Env[envFakePiSignal] = signalPath
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan string, 2)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 32)
			n, _ := conn.Read(buf)
			_ = conn.Close()
			signals <- strings.TrimSpace(string(buf[:n]))
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := RunWithOptions(ctx, p, "blocked task", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{})
		done <- err
	}()
	select {
	case got := <-signals:
		if got != "prompted" {
			t.Fatalf("first signal = %q", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("fake pi never signaled its prompt")
	case err := <-done:
		t.Fatalf("turn ended before the prompt was signaled: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("canceled turn did not finish")
	}
	log := readTestFile(t, logPath)
	if !strings.Contains(log, "ABORT") {
		t.Fatalf("canceled turn stopped pi without an RPC abort:\n%s", log)
	}
	if got := strings.Count(log, "SPAWN "); got != 1 {
		t.Fatalf("unexpected spawns = %d:\n%s", got, log)
	}
	// The fake acknowledges the abort and then starts the pending run; the
	// failed handshake must have stopped the process before the turn
	// returned, so that run can never execute unwatched.
	pid := fakePiPID(t, log)
	if proc, err := os.FindProcess(pid); err == nil {
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			t.Fatalf("fake pi %d survived the failed prompt handshake", pid)
		}
	}
}

// TestPiCompactRunsOnTheSessionProcess compacts a due session over its
// long-lived process instead of a second pi invocation.
func TestPiCompactRunsOnTheSessionProcess(t *testing.T) {
	logPath := installFakePi(t, false)
	p := fakePiProfile(logPath, false)
	p.Compact = &profile.Compact{Tokens: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workDir := t.TempDir()
	var sessionID string
	var compactions int
	r1, err := RunWithOptions(ctx, p, "grow the context", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{
		OnSession: func(id string) { sessionID = id },
	}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if r1.ContextTokens != 10 {
		t.Fatalf("first context tokens = %d", r1.ContextTokens)
	}
	r2, err := RunWithOptions(ctx, p, "after compaction", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{
		ResumeID:      sessionID,
		ContextTokens: r1.ContextTokens,
		OnCompaction:  func() { compactions++ },
	}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if r2.Output != "done: after compaction" {
		t.Fatalf("second output = %q", r2.Output)
	}
	log := readTestFile(t, logPath)
	if !strings.Contains(log, "COMPACT") {
		t.Fatalf("compact command never reached the session process:\n%s", log)
	}
	if got := strings.Count(log, "SPAWN "); got != 1 {
		t.Fatalf("compaction spawned another pi process; SPAWN count = %d:\n%s", got, log)
	}
	if compactions == 0 {
		t.Fatal("OnCompaction never ran")
	}
}

// TestPiInvocationSendsRPCCommands checks the launcher: rpc mode, the
// prompt off the argv, and a preassigned session id.
func TestPiInvocationSendsRPCCommands(t *testing.T) {
	inv, err := buildInvocationWithExecution(profile.Profile{Harness: profile.HarnessPi, Model: "zai/glm-5.3"}, "task", ExecutionOptions{DisableNativeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	fresh := piSessionArg(inv.initial.argv)
	if fresh == "" || !hasArg(inv.initial.argv, "--session-id") || !hasArg(inv.initial.argv, "--mode") || !hasArg(inv.initial.argv, "rpc") {
		t.Fatalf("initial argv = %v", inv.initial.argv)
	}
	if hasArg(inv.initial.argv, "--print") || strings.Contains(strings.Join(inv.initial.argv, " "), "task") {
		t.Fatalf("prompt or print mode leaked into argv: %v", inv.initial.argv)
	}
	if !inv.initial.rpc || inv.initial.prompt != "task" || inv.initial.parseOutput == nil {
		t.Fatalf("initial spec = %#v", inv.initial)
	}
	if id := inv.sessionID(""); id != fresh {
		t.Fatalf("sessionID = %q, want the preassigned %q", id, fresh)
	}
	resume := inv.resume(fresh, "next")
	if !resume.rpc || resume.prompt != "next" || !hasArg(resume.argv, "--session") || piSessionArg(resume.argv) != fresh {
		t.Fatalf("resume spec = %#v argv %v", resume, resume.argv)
	}
	compact := inv.compact(fresh)
	if !compact.rpc || compact.prompt != "" || piSessionArg(compact.argv) != fresh {
		t.Fatalf("compact spec = %#v argv %v", compact, compact.argv)
	}
}

// TestPiLaunchFingerprintIgnoresSessionSelector proves a resumed turn and a
// fresh turn of one session share a launch identity, while a model change
// or a different session dir does not.
func TestPiLaunchFingerprintIgnoresSessionSelector(t *testing.T) {
	env := []string{"PI_CODING_AGENT_DIR=/a", "PATH=/bin"}
	fresh := []string{"pi", "--mode", "rpc", "--session-id", "one", "--model", "m"}
	resume := []string{"pi", "--mode", "rpc", "--model", "m", "--session", "one"}
	base := piLaunchFingerprint("/pi", env, fresh)
	if got := piLaunchFingerprint("/pi", env, resume); got != base {
		t.Fatalf("resume fingerprint %q differs from fresh %q", got, base)
	}
	if got := piLaunchFingerprint("/pi", env, []string{"pi", "--mode", "rpc", "--session-id", "one", "--model", "other"}); got == base {
		t.Fatal("model change kept the fingerprint")
	}
	if got := piLaunchFingerprint("/pi2", env, fresh); got == base {
		t.Fatal("executable change kept the fingerprint")
	}
	if got := piLaunchFingerprint("/pi", append(append([]string(nil), env...), "NEW=1"), fresh); got == base {
		t.Fatal("environment change kept the fingerprint")
	}
	if piSessionKey(fresh, env) != piSessionKey(resume, env) {
		t.Fatal("fresh and resume sessions keyed differently")
	}
	if piSessionKey(fresh, []string{"PI_CODING_AGENT_DIR=/b", "PATH=/bin"}) == piSessionKey(fresh, env) {
		t.Fatal("two homes keyed the same session")
	}
}

func TestPiModelChangeRestartsTheSessionProcess(t *testing.T) {
	logPath := installFakePi(t, false)
	p := fakePiProfile(logPath, false)
	p.Model = "zai/glm-5.3"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workDir := t.TempDir()
	var sessionID string
	_, err := RunWithOptions(ctx, p, "first model", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{
		OnSession: func(id string) { sessionID = id },
	}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	p2 := fakePiProfile(logPath, false)
	p2.Model = "zai/glm-5.3-flash"
	r2, err := RunWithOptions(ctx, p2, "second model", workDir, false, JournalOptions{}, SteeringOptions{}, SessionOptions{ResumeID: sessionID}, ExecutionOptions{})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if r2.Output != "done: second model" {
		t.Fatalf("second output = %q", r2.Output)
	}
	log := readTestFile(t, logPath)
	spawns := strings.Count(log, "SPAWN ")
	if spawns != 2 {
		t.Fatalf("model change spawn count = %d:\n%s", spawns, log)
	}
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	second := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "SPAWN ") {
			second = line
		}
	}
	if !strings.Contains(second, "--session "+sessionID) || !strings.Contains(second, "--model zai/glm-5.3-flash") {
		t.Fatalf("restart missing resume session or model:\n%s", second)
	}
	if strings.Contains(second, "--print") || !strings.Contains(second, "--mode rpc") {
		t.Fatalf("restart did not use rpc mode:\n%s", second)
	}
}
