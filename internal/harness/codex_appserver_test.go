package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jsonrpc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

func TestCodexAppServerInvocation(t *testing.T) {
	p := profile.Profile{Harness: profile.HarnessCodex, Model: "gpt-6.1-sol", ExtraArgs: []string{
		"-c", "model_reasoning_effort=high", "-c", `mcp_servers={dyna={command="dyna",args=["mcp","shell"]}}`,
	}}
	inv, err := buildInvocationWithExecution(p, "first", ExecutionOptions{DisableNativeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.initial.argv, " ")
	if !hasArg(inv.initial.argv, "app-server") || hasArg(inv.initial.argv, "exec") {
		t.Fatalf("Codex must launch app-server, got %s", args)
	}
	for _, want := range []string{"--disable memories", "--disable multi_agent", "--disable multi_agent_v2", "--disable agent_message_board", "--disable goals", "--disable in_app_local_automation", "--disable remote_plugin", CodexCodeModeOverride, "model_reasoning_effort=high", "mcp_servers=", "features.codex_git_commit=false"} {
		if !strings.Contains(args, want) {
			t.Errorf("launch lacks %q: %s", want, args)
		}
	}
	if inv.initial.stdinPrompt || inv.initial.parseOutput == nil {
		t.Fatalf("app-server must use protocol output and input, got %+v", inv.initial)
	}
	if inv.resume == nil || inv.sessionID == nil {
		t.Fatal("missing exact thread continuation")
	}
	resumed := inv.resume("thread-123", "second")
	if resumed.prompt != "second" || strings.Join(resumed.argv, " ") != args {
		t.Fatalf("thread continuation must retain the same launch, got %+v", resumed)
	}
}

func TestCodexAppServerSandboxCanResume(t *testing.T) {
	for _, args := range [][]string{{"--sandbox", "read-only"}, {"--sandbox=workspace-write"}, {"-c", `default_permissions="dyna-journal"`}} {
		inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessCodex, SafeMode: true, ExtraArgs: args}, "read")
		if err != nil {
			t.Fatal(err)
		}
		if inv.resume == nil {
			t.Fatalf("app-server must resume with explicit permissions: %v", args)
		}
		if hasArg(inv.initial.argv, "--dangerously-bypass-approvals-and-sandbox") {
			t.Fatalf("widened explicit permissions: %v", inv.initial.argv)
		}
	}
}

func TestCodexAppServerTurnsReuseProcess(t *testing.T) {
	for _, mode := range []string{"normal", "already-initialized"} {
		t.Run(mode, func(t *testing.T) {
			p, logPath := installFakeCodex(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cwd := t.TempDir()
			var id string
			first, err := RunWithOptions(ctx, p, "one", cwd, false, JournalOptions{}, SteeringOptions{}, SessionOptions{OnSession: func(value string) { id = value }}, ExecutionOptions{})
			if err != nil || first.Output != "done: one" || id == "" {
				t.Fatalf("first turn = %+v, %v, thread %q", first, err, id)
			}
			second, err := RunWithOptions(ctx, p, "two", cwd, false, JournalOptions{}, SteeringOptions{}, SessionOptions{ResumeID: id, UsageBaseline: first.UsageBaseline}, ExecutionOptions{})
			if err != nil || second.Output != "done: two" {
				t.Fatalf("second turn = %+v, %v", second, err)
			}
			if second.Usage.InputTokens != 10 || second.Usage.OutputTokens != 5 {
				t.Fatalf("second usage = %+v", second.Usage)
			}
			log := readTestFile(t, logPath)
			if strings.Count(log, "SPAWN ") != 1 || strings.Count(log, `"method":"initialize"`) != 2 || strings.Count(log, `"method":"turn/start"`) != 2 || strings.Contains(log, `"method":"thread/resume"`) {
				t.Fatalf("process was not reused:\n%s", log)
			}
		})
	}
}

func TestCodexAppServerSteersNativeTurn(t *testing.T) {
	p, logPath := installFakeCodex(t, "hold")
	source := &armedSource{armed: true, message: "finish the tests"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var delivered []runstore.SteeringMessage
	result, err := RunWithOptions(ctx, p, "one", t.TempDir(), false, JournalOptions{}, SteeringOptions{Source: source, OnMessage: func(m runstore.SteeringMessage, _ *int64) { delivered = append(delivered, m) }}, SessionOptions{}, ExecutionOptions{})
	if err != nil || !strings.Contains(result.Output, "finish the tests") || len(delivered) != 1 {
		t.Fatalf("steered result = %+v, %v, deliveries %+v", result, err, delivered)
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "SPAWN ") != 1 || !strings.Contains(log, `"method":"turn/steer"`) || !strings.Contains(log, `"expectedTurnId":"turn-1"`) || strings.Contains(log, `"method":"turn/interrupt"`) {
		t.Fatalf("steering did not use the active native turn:\n%s", log)
	}
}

func TestCodexAppServerInterruptWaitsForCompletion(t *testing.T) {
	p, logPath := installFakeCodex(t, "hold")
	cwd := t.TempDir()
	path := filepath.Join(t.TempDir(), "gate.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	p.Env[envFakeCodexInterruptGate] = path
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	done := make(chan error, 1)
	var id string
	go func() {
		// An idle source keeps the steering supervisor that fires OnStart.
		_, err := RunWithOptions(ctx, p, "hold", cwd, false, JournalOptions{}, SteeringOptions{Source: &armedSource{}, OnStart: cancel}, SessionOptions{OnSession: func(value string) { id = value }}, ExecutionOptions{})
		done <- err
	}()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case err := <-done:
		t.Fatalf("returned before interrupt acknowledgment: %v", err)
	case <-timeout.C:
		t.Fatal("interrupt acknowledgment missing")
	}
	defer conn.Close()
	buf := make([]byte, 64)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("returned before turn/completed: %v", err)
	default:
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded unexpectedly")
		}
	case <-timeout.C:
		t.Fatal("interrupt did not finish")
	}
	log := readTestFile(t, logPath)
	if !strings.Contains(log, `"method":"turn/interrupt"`) || strings.Count(log, "SPAWN ") != 1 {
		t.Fatalf("missing native interrupt:\n%s", log)
	}
	// The terminal confirms the process is safe to reuse after cancellation.
	secondCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	source := &armedSource{armed: true, message: "next turn"}
	second, err := RunWithOptions(secondCtx, p, "next", cwd, false, JournalOptions{}, SteeringOptions{Source: source}, SessionOptions{ResumeID: id}, ExecutionOptions{})
	if err != nil || !strings.Contains(second.Output, "next turn") {
		t.Fatalf("turn after interrupt = %+v, %v", second, err)
	}
	if log = readTestFile(t, logPath); strings.Count(log, "SPAWN ") != 1 {
		t.Fatalf("interrupt replaced a healthy process:\n%s", log)
	}
}

func TestCodexAppServerConcurrentSessions(t *testing.T) {
	p, logPath := installFakeCodex(t, "hold")
	path := filepath.Join(t.TempDir(), "started.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	p.Env[envFakeCodexSignal] = path
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 32)
			_, _ = conn.Read(buf)
			_ = conn.Close()
			started <- struct{}{}
		}
	}()
	sources := [2]*testSource{{}, {}}
	type outcome struct {
		index  int
		result Result
		err    error
		thread string
	}
	done := make(chan outcome, 2)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	cwd := t.TempDir()
	for i := 0; i < 2; i++ {
		go func(i int) {
			var id string
			sessionProfile := p
			sessionProfile.Env = make(map[string]string, len(p.Env))
			for key, value := range p.Env {
				sessionProfile.Env[key] = value
			}
			sessionProfile.Env["DYNA_SESSION"] = fmt.Sprintf("concurrent-codex-%d", i)
			result, err := RunWithOptions(ctx, sessionProfile, "concurrent", cwd, false, JournalOptions{}, SteeringOptions{Source: sources[i], BeforeStart: func(context.Context, string) error {
				ready <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}, SessionOptions{OnSession: func(value string) { id = value }}, ExecutionOptions{})
			done <- outcome{i, result, err, id}
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("both sessions did not reach the start gate")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case got := <-done:
			t.Fatalf("session ended before both turns started: %+v", got)
		case <-ctx.Done():
			t.Fatal("concurrent startup stalled")
		}
	}
	sources[0].submit("alpha")
	sources[1].submit("beta")
	threads := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-done:
			want, other := "alpha", "beta"
			if got.index == 1 {
				want, other = other, want
			}
			if got.err != nil || !strings.Contains(got.result.Output, want) || strings.Contains(got.result.Output, other) {
				t.Fatalf("cross-session output: %+v", got)
			}
			if got.thread == "" || threads[got.thread] {
				t.Fatalf("thread collision: %+v", got)
			}
			threads[got.thread] = true
		case <-ctx.Done():
			t.Fatal("concurrent native steering stalled")
		}
	}
	log := readTestFile(t, logPath)
	if strings.Count(log, "SPAWN ") != 2 || strings.Count(log, `"method":"turn/steer"`) != 2 || strings.Contains(log, `"method":"turn/interrupt"`) {
		t.Fatalf("concurrent process routing:\n%s", log)
	}
}

func TestCodexAppServerNotificationTranslation(t *testing.T) {
	n := codexTurnNotifications{thread: "thread", turn: "turn"}
	update := func(method, params string) []string {
		return n.observe(jsonrpc.Notification{Method: method, Params: json.RawMessage(params)})
	}
	// Ignore other threads and turns, including background completions.
	if got := update("turn/completed", `{"threadId":"other","turn":{"id":"turn","status":"completed"}}`); len(got) != 0 || n.done {
		t.Fatal("accepted another thread")
	}
	lines := update("item/started", `{"threadId":"thread","turnId":"turn","item":{"id":"shell","type":"mcpToolCall","server":"dyna","tool":"shell","arguments":{"command":"pwd"}}}`)
	lines = append(lines, update("item/completed", `{"threadId":"thread","turnId":"turn","item":{"id":"shell","type":"mcpToolCall","server":"dyna","tool":"shell","arguments":{"command":"pwd"},"result":{"content":[{"type":"text","text":"/work"}]}}}`)...)
	lines = append(lines, update("item/completed", `{"threadId":"thread","turnId":"turn","item":{"id":"answer","type":"agentMessage","phase":"final_answer","text":"done"}}`)...)
	update("thread/tokenUsage/updated", `{"threadId":"thread","turnId":"turn","tokenUsage":{"total":{"inputTokens":100,"cachedInputTokens":20,"outputTokens":5}}}`)
	lines = append(lines, update("turn/completed", `{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)...)
	raw := strings.Join(lines, "\n")
	if !workeroutput.CodexTurnCompleted(raw) || codexAppServerOutput(raw) != "done" {
		t.Fatalf("invalid legacy stream: %s", raw)
	}
	var signals []workeroutput.ToolSignal
	for _, line := range lines {
		record := workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL}, line)
		signals = append(signals, record.Signals...)
	}
	if len(signals) != 2 || !strings.Contains(raw, `"input_tokens":100`) {
		t.Fatalf("lost tool boundaries or usage: %+v\n%s", signals, raw)
	}
}

// The hosted transport must keep the compaction item so the decoder emits its
// start and completion; a merged record lost both.
func TestCodexAppServerCompactionReachesDecoder(t *testing.T) {
	n := codexTurnNotifications{thread: "thread", turn: "turn"}
	var states []string
	for _, method := range []string{"item/started", "item/completed"} {
		params := `{"threadId":"thread","turnId":"turn","item":{"id":"c1","type":"contextCompaction"}}`
		for _, line := range n.observe(jsonrpc.Notification{Method: method, Params: json.RawMessage(params)}) {
			for _, e := range workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectCodexJSONL}, line).Events {
				if e.Kind == "status" && e.Name == "compaction" {
					states = append(states, e.State)
				}
			}
		}
	}
	if strings.Join(states, ",") != "started,completed" {
		t.Fatalf("compaction states = %v", states)
	}
}

func TestCodexAppServerLaunchChangeResumesThread(t *testing.T) {
	for _, change := range []string{"model", "account"} {
		t.Run(change, func(t *testing.T) {
			p, logPath := installFakeCodex(t, "normal")
			p.Model = "first-model"
			p.Env["CODEX_HOME"] = filepath.Join(t.TempDir(), "first-account")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cwd := t.TempDir()
			var id string
			first, err := RunWithOptions(ctx, p, "first", cwd, false, JournalOptions{}, SteeringOptions{}, SessionOptions{OnSession: func(value string) { id = value }}, ExecutionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if change == "model" {
				p.Model = "second-model"
			} else {
				p.Env["CODEX_HOME"] = filepath.Join(t.TempDir(), "second-account")
			}
			second, err := RunWithOptions(ctx, p, "second", cwd, false, JournalOptions{}, SteeringOptions{}, SessionOptions{ResumeID: id, UsageBaseline: first.UsageBaseline}, ExecutionOptions{})
			if err != nil || second.Output != "done: second" {
				t.Fatalf("resumed turn = %+v, %v", second, err)
			}
			log := readTestFile(t, logPath)
			if strings.Count(log, "SPAWN ") != 2 || strings.Count(log, `"method":"thread/resume"`) != 1 {
				t.Fatalf("launch change did not cold-resume:\n%s", log)
			}
		})
	}
}

// A thread whose Dyna shell MCP server failed to start has no Dyna tools. The
// turn must end with that reason instead of running blind.
func TestCodexDynaShellMCPFailureStopsTheTurn(t *testing.T) {
	p, logPath := installFakeCodex(t, "normal")
	p.ExtraArgs = []string{"-c", `mcp_servers={dyna={command="dyna",args=["mcp","shell"]}}`}
	p.Env[envFakeCodexMCPShellFail] = "1"

	_, err := runTest(context.Background(), p, "task", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Dyna shell MCP server dyna did not start") {
		t.Fatalf("Run() error = %v, want the Dyna shell MCP failure", err)
	}
	if got := fakeCodexRequests(readTestFile(t, logPath), "turn/start"); got != 0 {
		t.Fatalf("turn ran without Dyna tools: %d turn/start requests", got)
	}
}
