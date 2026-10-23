package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store/storetest"
)

// The fake Codex speaks the app-server JSON-RPC protocol by re-running this
// test binary, so assertions see the same records a real app-server sends.
const (
	envFakeCodex              = "DYNA_TEST_FAKE_CODEX"
	envFakeCodexMode          = "DYNA_TEST_FAKE_CODEX_MODE"
	envFakeCodexLog           = "DYNA_TEST_FAKE_CODEX_LOG"
	envFakeCodexFailStart     = "DYNA_TEST_FAKE_CODEX_FAIL_START"
	envFakeCodexFailTurns     = "DYNA_TEST_FAKE_CODEX_FAIL_TURNS"
	envFakeCodexFailMessages  = "DYNA_TEST_FAKE_CODEX_FAIL_MESSAGES"
	envFakeCodexFailOutput    = "DYNA_TEST_FAKE_CODEX_FAIL_OUTPUT"
	envFakeCodexQuota         = "DYNA_TEST_FAKE_CODEX_QUOTA"
	envFakeCodexEmpty         = "DYNA_TEST_FAKE_CODEX_EMPTY"
	envFakeCodexSignal        = "DYNA_TEST_FAKE_CODEX_SIGNAL"
	envFakeCodexInterruptGate = "DYNA_TEST_FAKE_CODEX_INTERRUPT_GATE"
	envFakeCodexSteerGate     = "DYNA_TEST_FAKE_CODEX_STEER_GATE"
	envFakeCodexMCPShellFail  = "DYNA_TEST_FAKE_CODEX_MCP_SHELL_FAIL"
)

// Modes select the base behavior; any other mode answers like "normal". Tests
// add the failure, quota, empty output and signal variables to the returned
// profile env for finer cases.
const (
	fakeCodexHold               = "hold"                // a turn waits for turn/steer or turn/interrupt
	fakeCodexAlreadyInitialized = "already-initialized" // initialize fails with -32600 "Already initialized"
)

type fakeCodexRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ThreadID       string `json:"threadId"`
		TurnID         string `json:"turnId"`
		ExpectedTurnID string `json:"expectedTurnId"`
		Model          string `json:"model"`
		Input          []struct {
			Text string `json:"text"`
		} `json:"input"`
	} `json:"params"`
}

// fakeCodexMain runs when the test binary is executed as `codex`. It logs a
// SPAWN line and every raw request line, then answers each request. The
// process stays alive until stdin closes, like a real app-server.
func fakeCodexMain() {
	logf := func(format string, args ...any) {
		f, err := os.OpenFile(os.Getenv(envFakeCodexLog), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return
		}
		fmt.Fprintf(f, format+"\n", args...)
		_ = f.Close()
	}
	args, _ := json.Marshal(os.Args[1:])
	logf("SPAWN %d %s", os.Getpid(), args)

	out := bufio.NewWriter(os.Stdout)
	emit := func(record map[string]any) {
		b, err := json.Marshal(record)
		if err != nil {
			return
		}
		_, _ = out.Write(append(b, '\n'))
		_ = out.Flush()
	}
	reply := func(id json.RawMessage, result any) { emit(map[string]any{"id": id, "result": result}) }
	replyError := func(id json.RawMessage, code int, message string) {
		emit(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
	}
	notify := func(method string, params map[string]any) { emit(map[string]any{"method": method, "params": params}) }

	mode := os.Getenv(envFakeCodexMode)
	failStart := os.Getenv(envFakeCodexFailStart) != ""
	failTurns, _ := strconv.Atoi(os.Getenv(envFakeCodexFailTurns))
	failMessages := []string{"connection reset by peer"}
	if v := os.Getenv(envFakeCodexFailMessages); v != "" {
		failMessages = strings.Split(v, "|")
	}
	failOutput := os.Getenv(envFakeCodexFailOutput)
	empty := os.Getenv(envFakeCodexEmpty) != ""
	quota := map[string]bool{}
	for _, model := range strings.Split(os.Getenv(envFakeCodexQuota), ",") {
		if model != "" {
			quota[model] = true
		}
	}
	// signal dials the test's listener when a turn starts, so the test gates
	// on a real event instead of polling the log.
	signal := func() {
		path := os.Getenv(envFakeCodexSignal)
		if path == "" {
			return
		}
		if conn, err := net.Dial("unix", path); err == nil {
			_, _ = conn.Write([]byte("prompted\n"))
			_ = conn.Close()
		}
	}

	// holdGate parks the fake at a request until the test closes the gate
	// connection. Without the gate's variable set, the fake answers at once.
	holdGate := func(key string) {
		path := os.Getenv(key)
		if path == "" {
			return
		}
		if conn, err := net.Dial("unix", path); err == nil {
			_, _ = conn.Write([]byte("ready\n"))
			var confirm [1]byte
			_, _ = conn.Read(confirm[:])
			_ = conn.Close()
		}
	}

	thread, active, threads, turns := "", "", 0, 0
	initialized := false
	agentMessage := func(turn, text string) {
		notify("item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{
			"type": "agentMessage", "id": "msg-" + turn, "text": text, "phase": "final_answer",
		}})
	}
	// finish ends a turn the way a successful app-server turn does: answer,
	// token totals, then completion.
	finish := func(turn, text string) {
		if !empty {
			agentMessage(turn, "done: "+text)
		}
		// Totals accumulate over the process, so a second turn reports both turns.
		notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{
			"total": map[string]any{"inputTokens": 10 * turns, "cachedInputTokens": 0, "outputTokens": 5 * turns},
		}})
		notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}})
	}
	inputText := func(req fakeCodexRequest) string {
		var text string
		for _, item := range req.Params.Input {
			text += item.Text
		}
		return text
	}

	handle := func(req fakeCodexRequest) {
		switch req.Method {
		case "initialize":
			if mode == fakeCodexAlreadyInitialized || initialized {
				replyError(req.ID, -32600, "Already initialized")
				return
			}
			initialized = true
			reply(req.ID, map[string]any{"userAgent": "fake-codex"})
		case "initialized":
		case "thread/start", "thread/resume":
			if failStart {
				replyError(req.ID, -32000, "fake thread start failed")
				return
			}
			if req.Method == "thread/resume" {
				thread = req.Params.ThreadID
			} else {
				threads++
				// The PID keeps ids unique across fake processes in one test binary.
				thread = fmt.Sprintf("fake-thread-%d-%d", os.Getpid(), threads)
			}
			if os.Getenv(envFakeCodexMCPShellFail) != "" {
				// A shell server that could not start leaves the thread with no
				// Dyna tools; a real app-server reports it before the first turn.
				notify("mcpServer/startupStatus/updated", map[string]any{"threadId": thread, "name": "dyna", "status": "failed",
					"error": "MCP startup failed: handshaking with MCP server failed"})
			}
			reply(req.ID, map[string]any{"thread": map[string]any{"id": thread}})
		case "turn/start":
			turns++
			turn := fmt.Sprintf("turn-%d", turns)
			if quota[req.Params.Model] {
				replyError(req.ID, -32000, "usage limit reached for model "+req.Params.Model)
				return
			}
			// A held turn is active before the reply, so an interrupt sent after
			// the reply always finds it.
			if mode == fakeCodexHold && turns > failTurns {
				active = turn
			}
			signal()
			reply(req.ID, map[string]any{"turn": map[string]any{"id": turn}})
			notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress"}})
			switch {
			case turns <= failTurns:
				if failOutput != "" {
					agentMessage(turn, failOutput)
				}
				notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{
					"id": turn, "status": "failed", "error": map[string]any{"message": failMessages[min(turns, len(failMessages))-1]},
				}})
			case mode == fakeCodexHold:
				// The turn stays active until turn/steer or turn/interrupt.
			default:
				finish(turn, inputText(req))
			}
		case "turn/steer":
			if active == "" || req.Params.ExpectedTurnID != active {
				replyError(req.ID, -32000, "expected turn is not active")
				return
			}
			// A held steer gets no normal answer. Its reply fails after the gate
			// opens, so the turn stays active until turn/interrupt.
			if os.Getenv(envFakeCodexSteerGate) != "" {
				holdGate(envFakeCodexSteerGate)
				replyError(req.ID, -32000, "steer released after hold")
				return
			}
			reply(req.ID, map[string]any{"turnId": active})
			finish(active, inputText(req))
			active = ""
		case "turn/interrupt":
			if active == "" || req.Params.TurnID != active {
				replyError(req.ID, -32000, "turn is not active")
				return
			}
			reply(req.ID, map[string]any{})
			holdGate(envFakeCodexInterruptGate)
			notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": active, "status": "interrupted"}})
			active = ""
		default:
			if len(req.ID) > 0 {
				replyError(req.ID, -32601, "unsupported Codex request "+req.Method)
			}
		}
	}

	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			logf("REQUEST %s", strings.TrimSpace(line))
			var req fakeCodexRequest
			if json.Unmarshal([]byte(line), &req) == nil {
				handle(req)
			}
		}
		if err != nil {
			return
		}
	}
}

// installFakeCodex puts a `codex` on PATH that re-runs the test binary in the
// given mode. It returns the profile and the log path that records the spawn
// and every request.
func installFakeCodex(t *testing.T, mode string) (profile.Profile, string) {
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
	logPath := filepath.Join(dir, "codex.log")
	script := "#!/bin/sh\nexec " + shellquote.Quote(exe) + " \"$@\"\n"
	if err := fsutil.WriteExecutable(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := map[string]string{"DYNA_SESSION": "test-codex-" + filepath.Base(dir), envFakeCodex: "1", envFakeCodexMode: mode, envFakeCodexLog: logPath}
	return profile.Profile{Name: "codex", Harness: profile.HarnessCodex, Env: env}, logPath
}

// fakeCodexGate listens on a unix socket and stores its path in env under key.
// The fake dials that socket at one point in a turn, and each dial arrives on
// the returned channel. Closing an arrived connection releases the fake.
func fakeCodexGate(t *testing.T, env map[string]string, key string) <-chan net.Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	env[key] = path
	dials := make(chan net.Conn, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case dials <- conn:
			default:
				// An unclaimed dial is closed, which releases the fake without a wait.
				_ = conn.Close()
			}
		}
	}()
	return dials
}

// fakeCodexSpawns counts the fake processes that started, from the fake's log.
func fakeCodexSpawns(log string) int {
	return strings.Count(log, "SPAWN ")
}

// fakeCodexRequests counts the requests with a method that reached a fake.
func fakeCodexRequests(log, method string) int {
	return strings.Count(log, `"method":"`+method+`"`)
}
