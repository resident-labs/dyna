package harness

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/acp"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// The fake ACP agent is the test binary re-executed on stdio.
// Its prompt scripts contain one JSON action per line:
//
//	{"waitForCancel":true}          wait for session/cancel
//	{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"..."}}}
//	{"update":{...},"promptId":"p1"} update tagged with a prompt id
//	{"usage":{"input_tokens":10,...}} emit a response_completed usage record
//	{"stopReason":"end_turn"}        settle the prompt
//
// Every boot and session/load appends a line to $FAKE_MARK so tests can
// assert process and session lifecycles.
func TestGrokAcpFakeAgent(t *testing.T) {
	if os.Getenv("GO_GROK_ACP_FAKE") != "1" {
		t.Skip("subprocess helper")
	}
	fakeMark(fmt.Sprintf("boot %d", os.Getpid()))
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "stdio" && os.Args[len(os.Args)-1] != "--uid=" && os.Args[len(os.Args)-1] != "acp" {
		t.Fatal("ACP child must use stdio")
	}
	fakeMark("stdio")
	(&fakeAgentServer{scriptDir: os.Getenv("FAKE_SCRIPT")}).serve(&fakeStdioConn{reader: bufio.NewReader(os.Stdin)})
}

func fakeMark(text string) {
	if path := os.Getenv("FAKE_MARK"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.WriteString(text + "\n")
			_ = f.Close()
			if strings.HasPrefix(text, "prompt ") || text == "cancel" {
				if conn, err := net.Dial("unixgram", path+".sock"); err == nil {
					_, _ = conn.Write([]byte(text))
					_ = conn.Close()
				}
			}
		}
	}
}

var acpTestEvents sync.Map // marker path -> *net.UnixConn

type fakeAgentServer struct {
	scriptDir string
}

func (s *fakeAgentServer) serve(conn fakeAgentConn) {
	defer conn.Close()
	session := os.Getenv("FAKE_SESSION_ID")
	if session == "" {
		session = "fake-session-1"
	}
	fakeMark("conn")
	cancel := make(chan struct{})
	ignoreCancel := false
	dropOnCancel := false
	var writeMu sync.Mutex
	write := func(value any) {
		data, _ := json.Marshal(value)
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteMessage(1, data)
	}
	// Prompts run one after another, as grok queues them; replies may still
	// arrive out of order through waitForReply.
	promptQueue := make(chan func(), 16)
	go func() {
		for work := range promptQueue {
			work()
		}
	}()
	active := 0
	var activeMu sync.Mutex
	beginPrompt := func() bool {
		activeMu.Lock()
		defer activeMu.Unlock()
		if os.Getenv("FAKE_KIMI") == "1" && active > 0 {
			return false
		}
		active++
		return true
	}
	endPrompt := func() {
		activeMu.Lock()
		active--
		activeMu.Unlock()
	}
	replied := map[string]chan struct{}{}
	repliedMu := sync.Mutex{}
	replyWritten := func(id string) <-chan struct{} {
		repliedMu.Lock()
		defer repliedMu.Unlock()
		if replied[id] == nil {
			replied[id] = make(chan struct{})
		}
		return replied[id]
	}
	markReplied := func(id string) {
		repliedMu.Lock()
		ch := replied[id]
		if ch == nil {
			ch = make(chan struct{})
			replied[id] = ch
		}
		repliedMu.Unlock()
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	for {
		kind, raw, err := conn.ReadMessage()
		if err != nil || kind != 1 {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		reply := func(id any, result any) {
			encoded, _ := json.Marshal(result)
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(encoded)})
		}
		switch msg.Method {
		case "initialize":
			var params struct{ ProtocolVersion int }
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark(fmt.Sprintf("initialize %d", params.ProtocolVersion))
			reply(msg.ID, map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "fake-auth"}}})
		case "authenticate":
			var params struct{ MethodID string }
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark("auth " + params.MethodID)
			reply(msg.ID, map[string]any{})
		case "session/new":
			var params struct {
				Meta struct {
					Agy struct{ DisabledTools []string }
				} `json:"_meta"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if len(params.Meta.Agy.DisabledTools) > 0 {
				fakeMark("disabled " + strings.Join(params.Meta.Agy.DisabledTools, ","))
			}
			write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "starting"}}}})
			reply(msg.ID, map[string]any{"sessionId": session, "models": map[string]any{"currentModelId": "fake-model"}, "modes": map[string]any{"currentModeId": "default", "availableModes": []any{}}})
		case "session/load", "session/resume":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark(msg.Method + " " + params.SessionID)
			if history, err := os.ReadFile(filepath.Join(s.scriptDir, "load.jsonl")); err == nil {
				s.runScript(write, params.SessionID, cancel, history)
			}
			reply(msg.ID, map[string]any{"sessionId": params.SessionID, "models": map[string]any{"currentModelId": "fake-model"}})
		case "session/set_mode":
			var params struct{ ModeID string }
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark("mode " + params.ModeID)
			reply(msg.ID, map[string]any{})
		case "session/set_model":
			var params struct {
				ModelID string `json:"modelId"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark("model " + params.ModelID)
			reply(msg.ID, map[string]any{})
		case "session/set_config_option":
			var params struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			fakeMark("config " + params.ConfigID + " " + params.Value)
			reply(msg.ID, map[string]any{})
		case "session/cancel":
			fakeMark("cancel")
			if dropOnCancel {
				// Fault injection: the connection dies exactly when the
				// cancel arrives, with the prompt's reply still pending.
				_ = conn.Close()
			}
			if !ignoreCancel {
				select {
				case <-cancel:
				default:
					close(cancel)
				}
			}
		case "session/prompt":
			// A previous canceled prompt must not cancel later steering or journal work.
			select {
			case <-cancel:
				cancel = make(chan struct{})
			default:
			}
			var params fakePromptParams
			_ = json.Unmarshal(msg.Params, &params)
			text := ""
			if len(params.Prompt) > 0 {
				text = params.Prompt[0].Text
			}
			fakeMark("prompt " + text)
			sum := sha256.Sum256([]byte(text))
			script := filepath.Join(s.scriptDir, hex.EncodeToString(sum[:8])[:12]+".jsonl")
			body, readErr := os.ReadFile(script)
			if readErr != nil {
				reply(msg.ID, map[string]any{"stopReason": "end_turn"})
				continue
			}
			if !beginPrompt() {
				fakeMark("busy")
				write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32600, "message": "another turn is already in progress"}})
				continue
			}
			ignoreCancel = strings.Contains(string(body), "ignoreCancel")
			promptID, dropAfter, waitFor := scriptFacts(body)
			dropOnCancel = dropAfter < 0
			cancelForPrompt := cancel
			promptQueue <- func() {
				defer endPrompt()
				stopReason, cancelled := s.runScript(write, session, cancelForPrompt, body)
				if cancelled {
					stopReason = "cancelled"
				}
				send := func() {
					// The mark sits inside the serialized write so the marker
					// stream records real reply order.
					data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
						"stopReason": stopReason,
						"_meta":      map[string]any{"promptId": promptID},
					}})
					writeMu.Lock()
					fakeMark("reply " + promptID)
					_ = conn.WriteMessage(1, data)
					writeMu.Unlock()
					markReplied(promptID)
				}
				if waitFor != "" {
					go func() {
						<-replyWritten(waitFor)
						send()
					}()
					return
				}

				send()
			}
		}
	}
}

// scriptFacts reads prompt correlation and event-driven fault controls.
func scriptFacts(script []byte) (promptID string, dropAfterMs int, waitForReply string) {
	var step struct {
		PromptID     string `json:"promptId"`
		DropAfterMs  int    `json:"dropConnectionAfterMs"`
		WaitForReply string `json:"waitForReply"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(script)), "\n") {
		if json.Unmarshal([]byte(line), &step) != nil {
			continue
		}
		if step.PromptID != "" && promptID == "" {
			promptID = step.PromptID
		}
		if step.DropAfterMs != 0 {
			dropAfterMs = step.DropAfterMs
		}
		if step.WaitForReply != "" {
			waitForReply = step.WaitForReply
		}
	}
	return promptID, dropAfterMs, waitForReply
}

// runScript plays one prompt script and returns the stop reason (empty means
// end_turn) and whether a cancel cut it short.
func (s *fakeAgentServer) runScript(write func(any), session string, cancel <-chan struct{}, script []byte) (string, bool) {
	ignoreCancel := strings.Contains(strings.TrimSpace(string(script)), "ignoreCancel")
	for _, action := range strings.Split(strings.TrimSpace(string(script)), "\n") {
		var step struct {
			Stderr        string         `json:"stderr"`
			WaitForCancel bool           `json:"waitForCancel"`
			Update        map[string]any `json:"update"`
			PromptID      string         `json:"promptId"`
			Stop          string         `json:"stopReason"`
			Usage         map[string]any `json:"usage"`
			IgnoreCancel  bool           `json:"ignoreCancel"`
		}
		if json.Unmarshal([]byte(action), &step) != nil {
			continue
		}
		if step.Stderr != "" {
			_, _ = fmt.Fprintln(os.Stderr, step.Stderr)
			continue
		}
		if step.WaitForCancel {
			if step.IgnoreCancel || ignoreCancel {
				select {}
			}
			<-cancel
			return "", true
		}
		if step.Update != nil {
			params := map[string]any{"sessionId": session, "update": step.Update}
			if step.PromptID != "" {
				params["_meta"] = map[string]any{"promptId": step.PromptID}
			}
			write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": params})
			continue
		}
		if step.Usage != nil {
			write(map[string]any{"jsonrpc": "2.0", "method": "_x.ai/session_notification", "params": map[string]any{"sessionId": session, "update": map[string]any{"sessionUpdate": "response_completed", "usage": step.Usage}}})
			continue
		}
		if step.Stop != "" {
			return step.Stop, false
		}
	}
	return "end_turn", false
}

// acpTestEnv installs the fake grok on PATH and returns the profile env and
// the marker file path. Each test gets its own private GROK_HOME so the
// hosted child lifecycle runs for real.
func acpTestEnv(t *testing.T, scriptDir string) (map[string]string, string) {
	t.Helper()
	bin := t.TempDir()
	home := t.TempDir()
	mark := filepath.Join(t.TempDir(), "mark")
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'grok 1.0.62 (fake) [stable]'; exit 0; fi\nGO_GROK_ACP_FAKE=1 FAKE_SCRIPT=%s FAKE_MARK=%s FAKE_SESSION_ID=%s exec '%s' -test.run=^TestGrokAcpFakeAgent$ -- \"$@\"\n", scriptDir, mark, os.Getenv("FAKE_SESSION_ID"), testBin)
	if err := os.WriteFile(filepath.Join(bin, "grok"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kimi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_STEER_HOOKS", "off")
	session := os.Getenv("FAKE_SESSION_ID")
	if session == "" {
		session = "fake-session-1"
	}
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: mark + ".sock", Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	acpTestEvents.Store(mark, listener)
	t.Cleanup(func() { sessionhost.ResetForTest(); listener.Close(); acpTestEvents.Delete(mark) })
	// A stable DYNA_SESSION keys the hosted child the way a Dyna session does.
	return map[string]string{"PATH": bin + ":" + os.Getenv("PATH"), "FAKE_SCRIPT": scriptDir, "FAKE_MARK": mark, "FAKE_SESSION_ID": session, "GROK_HOME": home, "DYNA_SESSION": "grok-test-" + filepath.Base(home)}, mark
}

type fakePromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"prompt"`
}

// acpScript writes one prompt script per prompt text, keyed by hash so the
// fake agent finds them across fresh processes.
func acpScript(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for text, script := range scripts {
		sum := sha256.Sum256([]byte(text))
		if err := os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:8])[:12]+".jsonl"), []byte(strings.ReplaceAll(script, "|", "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func acpProfile(env map[string]string) profile.Profile {
	p := profile.Profile{Name: "grok-fixture", Harness: profile.HarnessGrok}
	p.Env = env
	return p
}

// acpPrepare documents that the package's TestMain already runs turns on
// fakeruntime; the ACP tests need no override of their own and must not
// restore the production one.
func acpPrepare(t *testing.T) {
	t.Helper()
}

func TestGrokAcpArgsValidation(t *testing.T) {
	// The generated per-home flags stay valid; print-mode flags do not exist
	// on the stdio path.
	if err := validateGrokAcpArgs(profile.Profile{Harness: profile.HarnessGrok, ExtraArgs: []string{"--no-auto-update", "--no-leader", "--leader-socket", "/home/leader.sock"}}); err != nil {
		t.Fatalf("generated flags rejected: %v", err)
	}
	for _, flag := range []string{"--single", "--output-format", "--disallowed-tools", "--session-id", "-m", "--always-approve", "--permission-mode"} {
		if err := validateGrokAcpArgs(profile.Profile{Harness: profile.HarnessGrok, ExtraArgs: []string{flag}}); err == nil {
			t.Fatalf("accepted reserved %s", flag)
		}
	}
}

// Grok 1.0.62 exits with "unexpected argument '--no-auto-update'" when the
// generated flag follows the agent subcommand.
func TestGrokAcpArgvPutsTopLevelFlagsBeforeAgent(t *testing.T) {
	p := profile.Profile{Harness: profile.HarnessGrok, Env: map[string]string{"GROK_HOME": "/g"},
		ExtraArgs: []string{"--no-auto-update", "--no-leader", "--leader-socket", "/g/leader.sock", "--reasoning-effort", "high"}}
	argv, err := acpArgv(p)
	if err != nil {
		t.Fatal(err)
	}
	agent := slices.Index(argv, "agent")
	if update := slices.Index(argv, "--no-auto-update"); update < 0 || agent < 0 || update > agent {
		t.Fatalf("--no-auto-update must precede agent: %v", argv)
	}
	if effort := slices.Index(argv, "--reasoning-effort"); effort < agent || argv[len(argv)-1] != "stdio" {
		t.Fatalf("agent options must follow agent and end with stdio: %v", argv)
	}
	// Grok refuses a repeated --no-leader.
	leaders := 0
	for _, arg := range argv {
		if arg == "--no-leader" {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("--no-leader appears %d times: %v", leaders, argv)
	}
}

func TestGrokAcpTurnRunsOneAgentProcessAcrossTurns(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"first task":  `{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}}|{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer one"}},"promptId":"p1"}|{"usage":{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":10,"cache_creation_input_tokens":0}}|{"stopReason":"end_turn"}`,
		"second task": `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer two"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	zero := time.Duration(0)
	cwd := t.TempDir()
	first, err := RunWithOptions(t.Context(), acpProfile(env), "first task", cwd, true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("first turn: %v (result %+v)", err, first)
	}
	if first.Output != "answer one" || first.SessionID != "fake-session-1" {
		t.Fatalf("first=%q session=%q", first.Output, first.SessionID)
	}
	if first.Usage.InputTokens != 110 || first.Usage.OutputTokens != 5 {
		t.Fatalf("usage=%+v", first.Usage)
	}
	second, err := RunWithOptions(t.Context(), acpProfile(env), "second task", cwd, true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero, ResumeID: first.SessionID}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("second turn: %v (result %+v)", err, second)
	}
	if second.Output != "answer two" {
		t.Fatalf("second=%q", second.Output)
	}
	body, _ := os.ReadFile(mark)
	if boots := strings.Count(string(body), "boot "); boots != 1 {
		t.Fatalf("one long-lived agent expected, saw %d boots\n%s", boots, body)
	}
	if strings.Count(string(body), "session/load fake-session-1") != 1 {
		t.Fatalf("the live session was resumed once:\n%s", body)
	}
}

func TestGrokAcpSteeringQueuesNativePrompt(t *testing.T) {
	steered := steeringPrompt([]runstore.SteeringMessage{{Message: "check the new evidence"}})
	dir := acpScript(t, map[string]string{
		"long task": `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first answer"}},"promptId":"p1"}|{"stopReason":"end_turn","waitForReply":"p2"}`,
		steered:     `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"STEERED"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	source := &testSource{}
	delivered := make(chan runstore.SteeringMessage, 1)
	go func() {
		waitForMark(t, mark, "prompt long task")
		source.submit("check the new evidence")
	}()
	zero := time.Duration(0)
	result, err := RunWithOptions(t.Context(), acpProfile(env), "long task", t.TempDir(), true, JournalOptions{}, SteeringOptions{
		Source:    source,
		pollEvery: 15 * time.Millisecond,
		OnMessage: func(m runstore.SteeringMessage, _ *int64) { delivered <- m },
	}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("steered turn: %v (result %+v)", err, result)
	}
	if result.Output != "STEERED" {
		t.Fatalf("output=%q; the queued steering answer must win", result.Output)
	}
	select {
	case m := <-delivered:
		if m.Message != "check the new evidence" {
			t.Fatalf("delivered=%+v", m)
		}
	default:
		t.Fatal("steering message never confirmed")
	}
}

func TestGrokAcpResumesSessionInAFreshProcess(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"first":  `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"before restart"}},"promptId":"p1"}|{"stopReason":"end_turn"}`,
		"second": `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"after restart"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
	})
	t.Setenv("FAKE_SESSION_ID", "resume-me")
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	zero := time.Duration(0)
	cwd := t.TempDir()
	first, err := RunWithOptions(t.Context(), acpProfile(env), "first", cwd, true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.SessionID != "resume-me" {
		t.Fatalf("session=%q", first.SessionID)
	}
	sessionhost.ResetForTest() // the engine restart, or a stuck interrupt
	second, err := RunWithOptions(t.Context(), acpProfile(env), "second", cwd, true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero, ResumeID: first.SessionID}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("second: %v (result %+v)", err, second)
	}
	if second.Output != "after restart" {
		t.Fatalf("second=%q", second.Output)
	}
	body, _ := os.ReadFile(mark)
	if strings.Count(string(body), "session/load resume-me") != 1 {
		t.Fatalf("the cold session was restored:\n%s", body)
	}
	if boots := strings.Count(string(body), "boot "); boots != 2 {
		t.Fatalf("a fresh agent was expected, saw %d boots\n%s", boots, body)
	}
}

func TestGrokAcpStopReasonBecomesUsageLimit(t *testing.T) {
	// The turn must end on a usage limit. The failover after it has its own
	// test, and the real one installs a harness bundle in the data folder.
	oldFailover := FailoverAccount
	FailoverAccount = func(context.Context, profile.Profile, string, string, error) (*AccountSwitch, error) {
		return nil, nil
	}
	t.Cleanup(func() { FailoverAccount = oldFailover })
	dir := acpScript(t, map[string]string{"task": `{"stopReason":"rate_limit"}`})
	env, _ := acpTestEnv(t, dir)
	acpPrepare(t)
	zero := time.Duration(0)
	_, err := RunWithOptions(t.Context(), acpProfile(env), "task", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err == nil || !IsUsageLimit(err.Error()) {
		t.Fatalf("rate_limit classified as %v", err)
	}
}

func TestGrokAcpModelFallbackSwitchesModel(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"task":             `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"partial work"}},"promptId":"p1"}|{"stopReason":"rate_limit"}`,
		sessionNudgePrompt: `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"recovered"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	p := acpProfile(env)
	p.Models = []string{"first-model", "fallback-model"}
	zero := time.Duration(0)
	result, err := RunWithOptions(t.Context(), p, "task", t.TempDir(), true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("fallback turn: %v (result %+v)", err, result)
	}
	if result.Output != "recovered" || result.Model != "fallback-model" {
		t.Fatalf("output=%q model=%q", result.Output, result.Model)
	}
	body, _ := os.ReadFile(mark)
	if !strings.Contains(string(body), "model fallback-model") {
		t.Fatalf("session/set_model missing:\n%s", body)
	}
}

func TestGrokAcpJournalReminderInterrupts(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"quiet task":            `{"waitForCancel":true}`,
		journalNudgePrompt:      `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"nudged answer"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
		journalCompletionPrompt: `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"reminder answer"}},"promptId":"p3"}|{"stopReason":"end_turn"}`,
	})
	env, _ := acpTestEnv(t, dir)
	acpPrepare(t)
	journal := JournalOptions{Path: filepath.Join(t.TempDir(), "journal.jsonl"), IdleAfter: 60 * time.Millisecond}
	result, err := RunWithJournal(t.Context(), acpProfile(env), "quiet task", t.TempDir(), true, journal)
	if err != nil {
		t.Fatalf("journal turn: %v (result %+v)", err, result)
	}
	if result.Output != "nudged answer" {
		t.Fatalf("output=%q; the interrupted task's answer must survive the reminder", result.Output)
	}
	if result.JournalNudges < 1 {
		t.Fatalf("journal nudges=%d", result.JournalNudges)
	}
}

// waitForMark waits on a datagram emitted after the fake commits its marker.
func waitForMark(t *testing.T, mark, want string) {
	t.Helper()
	if body, err := os.ReadFile(mark); err == nil && strings.Contains(string(body), want) {
		return
	}
	value, ok := acpTestEvents.Load(mark)
	if !ok {
		t.Fatal("missing fake event socket")
	}
	conn := value.(*net.UnixConn)
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var data [4096]byte
	for {
		n, err := conn.Read(data[:])
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		if strings.Contains(string(data[:n]), want) {
			return
		}
	}
}

func assertACPAgentStopped(t *testing.T, mark string) {
	t.Helper()
	data, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "boot ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(line, "boot "))
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatalf("ACP child %d still exists after stop", pid)
		}
	}
}

// A turn canceled against an agent that ignores session/cancel must stop the
// session child instead of returning while the prompt runs on.
func TestGrokAcpUnconfirmedCancelStopsTheAgent(t *testing.T) {
	acw := acpCancelWait
	acpCancelWait = 150 * time.Millisecond
	t.Cleanup(func() { acpCancelWait = acw })
	dir := acpScript(t, map[string]string{"stubborn task": `{"ignoreCancel":true,"waitForCancel":true}`})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		waitForMark(t, mark, "prompt stubborn task")
		cancel()
	}()
	_, err := RunWithOptions(ctx, acpProfile(env), "stubborn task", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err == nil {
		t.Fatal("a canceled turn must fail")
	}
	// The agent received the cancel and ignored it, and the unconfirmed
	// cancel stopped that session child.
	body, _ := os.ReadFile(mark)
	if !strings.Contains(string(body), "cancel") {
		t.Fatalf("the agent never received the cancel:\n%s", body)
	}
	assertACPAgentStopped(t, mark)
}

// A pipe that closes during cancellation cannot confirm remote completion.
// The host must stop that session child.
func TestGrokAcpDisconnectDuringCancelStopsTheAgent(t *testing.T) {
	acw := acpCancelWait
	acpCancelWait = 5 * time.Second
	t.Cleanup(func() { acpCancelWait = acw })
	dir := acpScript(t, map[string]string{"dropped task": `{"ignoreCancel":true,"waitForCancel":true,"dropConnectionAfterMs":-1}`})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	ctx, cancel := context.WithCancel(t.Context())
	coordinated := make(chan struct{})
	go func() {
		defer close(coordinated)
		// Cancel once the prompt is running; the fake then closes the
		// connection from its own cancel handler, reply still pending.
		waitForMark(t, mark, "prompt dropped task")
		cancel()
	}()
	_, err := RunWithOptions(ctx, acpProfile(env), "dropped task", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	<-coordinated
	if err == nil {
		t.Fatal("a turn whose connection dropped must fail")
	}
	if body, _ := os.ReadFile(mark); !strings.Contains(string(body), "cancel") {
		t.Fatalf("the cancel was never received:\n%s", body)
	}
	assertACPAgentStopped(t, mark)
}

// Replies can arrive out of submission order; the newest prompt still owns
// the turn's answer.
func TestGrokAcpReversedReplyOrderStillPicksTheNewestPrompt(t *testing.T) {
	steered := steeringPrompt([]runstore.SteeringMessage{{Message: "reverse me"}})
	dir := acpScript(t, map[string]string{
		// The first prompt's reply is held until the steering prompt's
		// reply has been written: completion is reversed by construction.
		"reversed task": `{"promptId":"p1"}|{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first answer"}},"promptId":"p1"}|{"stopReason":"end_turn","waitForReply":"p2"}`,
		steered:         `{"promptId":"p2"}|{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"STEERED"}},"promptId":"p2"}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	source := &testSource{}
	go func() {
		// Steering joins the running prompt's attempt, not a new one.
		waitForMark(t, mark, "prompt reversed task")
		source.submit("reverse me")
	}()
	result, err := RunWithOptions(t.Context(), acpProfile(env), "reversed task", t.TempDir(), true, JournalOptions{}, SteeringOptions{Source: source, pollEvery: 10 * time.Millisecond}, SessionOptions{}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second})
	if err != nil {
		t.Fatalf("reversed turn: %v", err)
	}
	if result.Output != "STEERED" {
		t.Fatalf("output=%q; the newest prompt must win despite reply order", result.Output)
	}
	body, _ := os.ReadFile(mark)
	replyAt := func(id string) int {
		for i, line := range strings.Split(string(body), "\n") {
			if line == "reply "+id {
				return i
			}
		}
		return -1
	}
	if replyAt("p2") == -1 || replyAt("p1") == -1 || replyAt("p2") > replyAt("p1") {
		t.Fatalf("completion was not reversed: p1@%d p2@%d\n%s", replyAt("p1"), replyAt("p2"), body)
	}
	if n := strings.Count(string(body), "conn"); n != 1 {
		t.Fatalf("the turn must stay in one attempt (one connection, saw %d):\n%s", n, body)
	}
}

// A startup timeout must cancel and confirm the prompt the agent accepted.
func TestGrokAcpStartupTimeoutCancelsTheAcceptedPrompt(t *testing.T) {
	dir := acpScript(t, map[string]string{"quiet startup": `{"waitForCancel":true}`})
	env, mark := acpTestEnv(t, dir)
	acpPrepare(t)
	startupFired := make(chan time.Time)
	go func() {
		waitForMark(t, mark, "prompt quiet startup")
		close(startupFired)
	}()
	_, err := RunWithOptions(t.Context(), acpProfile(env), "quiet startup", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{HealthProbe: true, startupLimit: 30 * time.Second, startupFired: startupFired})
	if err == nil || !strings.Contains(err.Error(), "startup") {
		t.Fatalf("startup timeout lost: %v", err)
	}
	waitForMark(t, mark, "cancel")
}

// fakeAgentConn lets the same fake protocol run before and after the transport migration.
type fakeAgentConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}
type fakeStdioConn struct{ reader *bufio.Reader }

func (c *fakeStdioConn) ReadMessage() (int, []byte, error) {
	data, err := c.reader.ReadBytes('\n')
	return 1, data, err
}
func (*fakeStdioConn) WriteMessage(_ int, data []byte) error {
	_, err := os.Stdout.Write(append(data, '\n'))
	return err
}
func (*fakeStdioConn) Close() error { _ = os.Stdout.Close(); return os.Stdin.Close() }

func TestGrokAcpConcurrentSessionsUseSeparateStdioChildren(t *testing.T) {
	dir := acpScript(t, map[string]string{"task": "{\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"answer\"}}}|{\"stopReason\":\"end_turn\"}"})
	env, mark := acpTestEnv(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type result struct {
		session *acpSession
		answer  string
		err     error
	}
	ready := make(chan result, 2)
	start := make(chan struct{})
	for _, identity := range []string{"session-a", "session-b"} {
		p := acpProfile(map[string]string{})
		for k, v := range env {
			p.Env[k] = v
		}
		p.Env["DYNA_SESSION"] = identity
		p.RuntimeExecutable = filepath.Join(strings.Split(env["PATH"], ":")[0], "grok")
		p.RuntimeBindProcess = func(json.RawMessage) error { return nil }
		go func() {
			<-start
			accumulator := workeroutput.AcpOutput()
			s, err := acpDial(ctx, p, dir, "", ExecutionOptions{HealthProbe: true}, func(u acp.Update) { accumulator.Observe(string(u.Raw)) }, nil)
			if err == nil {
				_, err = s.agent.Prompt(ctx, s.session, []acp.PromptBlock{acp.TextBlock("task")})
			}
			ready <- result{s, accumulator.Take(""), err}
		}()
	}
	close(start)
	for range 2 {
		select {
		case r := <-ready:
			if r.session != nil {
				defer r.session.close()
			}
			if r.err != nil {
				t.Fatalf("concurrent ACP session: %v", r.err)
			}
			if r.answer != "answer" {
				t.Fatalf("concurrent ACP answer: %q", r.answer)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	body, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "boot ") != 2 || strings.Count(string(body), "stdio\n") != 2 {
		t.Fatalf("two sessions must own two stdio children, got:\n%s", body)
	}
}

func TestAntigravityACPReusesStdioHostAcrossTurns(t *testing.T) {
	dir := acpScript(t, map[string]string{"task": "{\"stopReason\":\"end_turn\"}"})
	p, mark := antigravityACPTestProfile(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	nativeID := ""
	for range 2 {
		s, err := acpDial(ctx, p, dir, nativeID, ExecutionOptions{HealthProbe: true, DynaShell: true}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		nativeID = s.session
		if _, err := s.agent.Prompt(ctx, s.session, []acp.PromptBlock{acp.TextBlock("task")}); err != nil {
			s.close()
			t.Fatal(err)
		}
		s.settled = true
		s.close()
	}
	body, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	for text, want := range map[string]int{"boot ": 1, "initialize 2": 1, "auth oauth-personal": 1, "session/resume fake-session-1": 1, "disabled start_subagent,schedule,run_command": 1, "mode yolo": 2} {
		if got := strings.Count(string(body), text); got != want {
			t.Fatalf("%q count=%d, want %d:\n%s", text, got, want, body)
		}
	}
}

func TestGrokACPHardCancelStopsWithoutWaitingForProtocolGrace(t *testing.T) {
	dir := acpScript(t, map[string]string{"hard deadline": `{"ignoreCancel":true,"waitForCancel":true}`})
	env, mark := acpTestEnv(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { waitForMark(t, mark, "prompt hard deadline"); cancel() }()
	_, err := RunWithOptions(ctx, acpProfile(env), "hard deadline", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{HealthProbe: true, KillOnCancel: true, startupLimit: 30 * time.Second})
	if err == nil {
		t.Fatal("hard deadline returned success")
	}
	assertACPAgentStopped(t, mark)
	body, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "cancel\n") {
		t.Fatalf("hard deadline waited on session/cancel instead of killing the child:\n%s", body)
	}
}

func antigravityACPTestProfile(t *testing.T, scriptDir string) (profile.Profile, string) {
	t.Helper()
	env, mark := acpTestEnv(t, scriptDir)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "antigravity-acp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "antigravity-acp", "acp_token.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	env["HOME"] = home
	cli := filepath.Join(strings.Split(env["PATH"], ":")[0], "grok")
	if err := os.Symlink(cli, filepath.Join(filepath.Dir(cli), "agy_acp_server.par")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(cli), "localharness_external"), []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessAntigravity, Env: env, RuntimeExecutable: cli, RuntimeBindProcess: func(json.RawMessage) error { return nil }}

	return p, mark
}

func TestAntigravityACPReadsBackendErrorMarkersBeforeSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		wantError    bool
	}{
		{"current-session", "Antigravity SDK error processing prompt for session fake-session-1\nResourceExhausted: quota exhausted", true},
		{"another-session", "Antigravity SDK error processing prompt for session fake-session-10", false},
		{"ordinary-text", "The assistant discussed a quota exhausted error.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := json.Marshal(map[string]string{"stderr": tc.stderr})
			if err != nil {
				t.Fatal(err)
			}
			script := string(first) + "|" + `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}|{"stopReason":"end_turn"}`
			dir := acpScript(t, map[string]string{"task": script})
			p, _ := antigravityACPTestProfile(t, dir)
			declaration := &workeroutput.Declaration{Dialect: workeroutput.DialectAcpJSON}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			result := runAcpAttempt(ctx, p, dir, commandSpec{acpPrompt: "task"}, &sessionTracker{}, nil, nil, false, "", "", declaration, nil, newDeliveryGate(JournalOptions{}), &runState{}, ExecutionOptions{HealthProbe: true})
			if (result.runErr != nil) != tc.wantError {
				t.Fatalf("backend error=%v, want error=%v", result.runErr, tc.wantError)
			}
			if tc.wantError && !IsUsageLimit(result.runErr.Error()) {
				t.Fatalf("quota classification lost: %v", result.runErr)
			}
		})
	}
}
