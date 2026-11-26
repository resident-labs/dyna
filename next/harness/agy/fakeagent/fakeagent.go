// Package fakeagent is a scripted stand-in for `agy acp`, for tests. A test binary runs it when re-executed as the agent
// (see Run). Message shapes are the ones the spike recorded from agy 1.5.18 in /box/shared/agy-spike.
//
// The first text block of a prompt picks the behaviour:
//
//	say <text>     reply with the text, in two chunks
//	think          a thought chunk, then "thought"
//	tool           a tool call that asks permission, completes, then "tool done"
//	hold           run until cancelled, or until another prompt steers it
//	stubborn       run forever and ignore session/cancel
//	quota          an upstream 429 as the chat text, then end_turn
//	forbidden      an upstream 403 as the chat text
//	validation     Google's VALIDATION_REQUIRED error body
//	fivexx         an upstream 503
//	tos            a TOS_VIOLATION as the chat text
//	rpcerror       a JSON-RPC error answer
//	refusal        stopReason refusal
//	maxtokens      stopReason max_tokens
//	crash          exit at once
//	/compact       reply "compacted"
//
// Environment: FAKE_AGY_LOG (file every received line is appended to, the answers to its permission requests included),
// FAKE_AGY_VERSION, FAKE_AGY_NO_APPEND, FAKE_AGY_AUTH_FAIL (FAKE_AGY_AUTH_SUBSCRIPTION: the error is SUBSCRIPTION_REQUIRED), FAKE_AGY_MISSING (a session id resume refuses),
// FAKE_AGY_STARTUP_PERMISSION (ask permission once the session exists, with no prompt open), FAKE_AGY_NO_COMPACT, FAKE_AGY_REAUTH (session setup answers -32000 until authenticate has run twice).
package fakeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/harness/agy/sessiondb"
)

// EnvVar is what a test sets in the re-executed process so its TestMain calls Run.
const EnvVar = "AGY_FAKE_AGENT"

type agent struct {
	out    io.Writer
	writeM sync.Mutex
	logM   sync.Mutex
	log    *os.File

	mu        sync.Mutex
	sid       string // the session the updates are for
	gemini    string // --gemini-dir
	sessions  int
	authCount int
	model     string
	thought   string
	held      chan string // the running "hold" prompt waits on this: "cancel" or "steer"
}

// Run serves the agent protocol on in and out until in ends.
func Run(in io.Reader, out io.Writer) {
	a := &agent{out: out, model: "gemini-4-neon", thought: "high"}
	for i, arg := range os.Args {
		if arg == "--gemini-dir" && i+1 < len(os.Args) {
			a.gemini = os.Args[i+1]
		}
	}
	if path := os.Getenv("FAKE_AGY_LOG"); path != "" {
		a.log, _ = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	}
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		a.record(strings.TrimSpace(string(line)))
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(line, &m) != nil || m.Method == "" {
			continue
		}
		a.handle(m.ID, m.Method, m.Params)
	}
}

func (a *agent) record(line string) {
	if a.log == nil {
		return
	}
	a.logM.Lock()
	defer a.logM.Unlock()
	fmt.Fprintln(a.log, line)
}

func (a *agent) send(v map[string]any) {
	v["jsonrpc"] = "2.0"
	data, _ := json.Marshal(v)
	a.writeM.Lock()
	defer a.writeM.Unlock()
	a.out.Write(append(data, '\n'))
}

func (a *agent) reply(id json.RawMessage, result any) {
	a.send(map[string]any{"id": id, "result": result})
}

func (a *agent) fail(id json.RawMessage, code int, message string) {
	a.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

func (a *agent) session() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sid
}

func (a *agent) update(update map[string]any) {
	a.send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": a.session(), "update": update}})
}

// step writes a step into the session's log the way agy does, before the prompt's response.
func (a *agent) step(stepType int, payload string) {
	if err := sessiondb.AppendStep(a.gemini, a.session(), stepType, payload); err != nil {
		panic(err)
	}
}

func (a *agent) options() []map[string]any {
	choices := func(values ...string) []map[string]any {
		var out []map[string]any
		for _, v := range values {
			out = append(out, map[string]any{"value": v, "name": v})
		}
		return out
	}
	return []map[string]any{
		{"id": "model", "category": "model", "type": "select", "currentValue": a.model, "options": choices("gemini-4-neon", "gemini-3.8-flash", "claude-sonnet-5-5")},
		{"id": "thought_level", "category": "thought_level", "type": "select", "currentValue": a.thought, "options": choices("low", "medium", "high", "xhigh")},
	}
}

func (a *agent) handle(id json.RawMessage, method string, raw json.RawMessage) {
	switch method {
	case "initialize":
		// The client asks for protocol 2 as T3 does; the real agy answers 1 whatever it is asked.
		var ask struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		json.Unmarshal(raw, &ask)
		if ask.ProtocolVersion != 2 {
			a.fail(id, -32602, "unsupported protocol version")
			return
		}
		version := os.Getenv("FAKE_AGY_VERSION")
		if version == "" {
			version = "1.5.18"
		}
		meta := map[string]any{"antigravity": map[string]any{"tier": true, "systemPrompt": map[string]any{"append": true}}}
		resume := map[string]any{"resume": map[string]any{}}
		if os.Getenv("FAKE_AGY_NO_RESUME") != "" {
			resume = map[string]any{}
		}
		a.reply(id, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"image": true, "audio": false, "embeddedContext": true}, "sessionCapabilities": resume, "_meta": meta},
			"agentInfo":         map[string]any{"name": "Antigravity ACP Server", "version": version},
			"authMethods":       []map[string]any{{"id": "oauth-personal", "name": "Log in with Google"}},
		})
	case "authenticate":
		a.authCount++
		if os.Getenv("FAKE_AGY_AUTH_SUBSCRIPTION") != "" {
			a.fail(id, -32603, "SUBSCRIPTION_REQUIRED")
			return
		}
		if os.Getenv("FAKE_AGY_AUTH_FAIL") != "" {
			a.fail(id, -32603, "No browser is available here to sign in")
			return
		}
		a.reply(id, map[string]any{"_meta": map[string]any{"antigravity": map[string]any{"tier": map[string]any{"name": "fake"}}}})
	case "session/new", "session/resume":
		if method == "session/resume" && os.Getenv("FAKE_AGY_RESUME_RESTRICTION") != "" {
			a.fail(id, -32603, "Error 403, Message: Your account is restricted (TOS_VIOLATION), Status: PERMISSION_DENIED")
			return
		}
		if os.Getenv("FAKE_AGY_REAUTH") != "" && a.authCount < 2 {
			a.fail(id, -32000, "authentication required")
			return
		}
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(raw, &p)
		if method == "session/resume" && p.SessionID != "" && p.SessionID == os.Getenv("FAKE_AGY_MISSING") {
			a.fail(id, -32602, "session not found")
			return
		}
		a.mu.Lock()
		a.sessions++
		sid := fmt.Sprintf("fake-session-%d", a.sessions)
		if method == "session/resume" {
			sid = p.SessionID
		}
		a.sid = sid
		a.mu.Unlock()
		// agy has created its conversations directory by now.
		os.MkdirAll(filepath.Join(a.gemini, "antigravity-acp", "conversations"), 0o700)
		mode := "default"
		if m := os.Getenv("FAKE_AGY_MODE"); m != "" {
			mode = m
		}
		result := map[string]any{"configOptions": a.options(), "modes": map[string]any{"currentModeId": mode, "availableModes": []map[string]any{{"id": "default"}, {"id": "plan"}}}}
		if method == "session/new" {
			result["sessionId"] = sid
		}
		a.reply(id, result)
		cmds := []map[string]any{{"name": "goal"}}
		if os.Getenv("FAKE_AGY_NO_COMPACT") == "" {
			cmds = append(cmds, map[string]any{"name": "compact"})
		}
		a.update(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": cmds})
		if os.Getenv("FAKE_AGY_STARTUP_PERMISSION") != "" {
			go a.permission("startup")
		}
	case "session/set_mode":
		a.reply(id, map[string]any{})
	case "session/set_config_option":
		var p struct{ ConfigID, Value string }
		json.Unmarshal(raw, &p)
		if p.ConfigID == "thought_level" && os.Getenv("FAKE_AGY_FAIL_THOUGHT") != "" {
			a.fail(id, -32602, "invalid thought level")
			return
		}
		a.mu.Lock()
		if p.ConfigID == "model" {
			a.model = p.Value
		} else {
			a.thought = p.Value
		}
		a.mu.Unlock()
		a.reply(id, map[string]any{"configOptions": a.options()})
	case "session/cancel":
		a.mu.Lock()
		held := a.held
		a.mu.Unlock()
		if held != nil {
			select {
			case held <- "cancel":
			default:
			}
		}
	case "session/prompt":
		// Whether this prompt steers a running one is decided here, in the order the prompts arrive.
		var p struct {
			Prompt []struct{ Text string } `json:"prompt"`
		}
		json.Unmarshal(raw, &p)
		// The user text is the last block that is not the runtime info, inside <user_request> the first time.
		text := ""
		for _, b := range p.Prompt {
			if !strings.HasPrefix(b.Text, "<runtime_info>") && !strings.HasPrefix(b.Text, "<external_") {
				text = b.Text
			}
		}
		if _, rest, ok := strings.Cut(text, "<user_request>\n"); ok {
			text = strings.TrimSuffix(rest, "\n</user_request>")
		}
		// A handoff puts the history ahead of the user's text (handoff.Prompt).
		if _, rest, ok := strings.Cut(text, "\n\nUser message:\n"); ok {
			text = rest
		}
		verb, _, _ := strings.Cut(text, " ")
		a.mu.Lock()
		steered := a.held
		if steered != nil && text == "stubborn" {
			steered = nil
		}
		var held chan string
		if steered == nil && (verb == "hold" || verb == "stubborn" || verb == "holdperm") {
			held = make(chan string, 4)
			a.held = held
		}
		a.mu.Unlock()
		go a.prompt(id, text, steered, held)
	default:
		if len(id) > 0 {
			a.fail(id, -32601, "method not found")
		}
	}
}

// permission asks as agy does for a shell command and logs the answer.
func (a *agent) permission(title string) {
	a.send(map[string]any{"id": "perm-" + title, "method": "session/request_permission", "params": map[string]any{
		"sessionId": a.session(),
		"toolCall":  map[string]any{"toolCallId": "call_1", "name": "run_command", "kind": "execute", "title": title, "rawInput": map[string]any{"CommandLine": title}},
		"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}, {"optionId": "deny", "name": "Deny", "kind": "reject_once"}},
	}})
}

var usage = map[string]any{"inputTokens": 100, "outputTokens": 20, "cachedReadTokens": 50, "cachedWriteTokens": 5, "thoughtTokens": 7, "totalTokens": 175}

func (a *agent) end(id json.RawMessage, reason string) {
	a.reply(id, map[string]any{"stopReason": reason, "usage": usage})
}

func (a *agent) text(s string) {
	a.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": s}})
}

func (a *agent) prompt(id json.RawMessage, text string, steered, held chan string) {
	if steered != nil {
		a.text("steer seen")
		a.end(id, "end_turn")
		steered <- "steer"
		return
	}
	verb, rest, _ := strings.Cut(text, " ")
	switch verb {
	case "say":
		half := len(rest) / 2
		a.text(rest[:half])
		a.text(rest[half:])
		a.end(id, "end_turn")
	case "think":
		a.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hmm"}})
		a.text("thought")
		a.end(id, "end_turn")
	case "tool":
		input := map[string]any{"CommandLine": "ls", "toolSummary": "List files"}
		call := map[string]any{"toolCallId": "call_1", "name": "run_command", "kind": "execute", "title": "ls", "rawInput": input}
		a.text("looking")
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call", "status": "in_progress"}))
		a.permission("ls")
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call_update", "status": "completed", "rawOutput": map[string]any{"output": "a.txt\n", "exit_code": 0}}))
		a.text("tool done")
		a.end(id, "end_turn")
	case "hold", "stubborn", "holdperm":
		a.text("working")
		if verb == "stubborn" {
			select {}
		}
		reason := <-held
		a.mu.Lock()
		a.held = nil
		a.mu.Unlock()
		if reason == "cancel" {
			if verb == "holdperm" {
				a.permission("aborting") // asked while the aborted turn settles
				time.Sleep(100 * time.Millisecond)
			}
			a.end(id, "cancelled")
			return
		}
		a.text("after steer")
		a.end(id, "end_turn")
	// An upstream failure: agy shows it as chat text and ends the turn, and writes an error step to the session log.
	case "quota":
		a.failStep(id, "Error 429, Message: You have exhausted your capacity on this model. Your quota will reset after 4m42s. Resets in 4m42s., Status: RESOURCE_EXHAUSTED", false)
	case "forbidden":
		a.failStep(id, "Error 403, Message: The caller does not have permission, Status: PERMISSION_DENIED", false)
	case "tos":
		a.failStep(id, "Error 403, Message: Your account is restricted (TOS_VIOLATION), Status: PERMISSION_DENIED", false)
	case "validation":
		a.failStep(id, "{\"error\": {\"code\": 403, \"status\": \"PERMISSION_DENIED\", \"details\": [{\"@type\": \"type.googleapis.com/google.rpc.ErrorInfo\", \"reason\": \"VALIDATION_REQUIRED\", \"domain\": \"cloudcode-pa.googleapis.com\", \"metadata\": {\"validation_url\": \"https://accounts.google.com/signin/continue?token=x\"}}]}}", false)
	case "fivexx":
		a.failStep(id, "Error 503, Message: backend unavailable, Status: UNAVAILABLE", false)
	case "notfound":
		a.failStep(id, "Error 404, Message: not found, Status: NOT_FOUND", false)
	case "notfoundlate":
		a.failStep(id, "Error 404, Message: not found, Status: NOT_FOUND", true)
	case "signature":
		a.failStep(id, "Error 400, Message: messages.3.content.0.thinking.signature: Field required, Status: INVALID_ARGUMENT", true)
	case "odd":
		a.failStep(id, "something nobody can parse", false)
	// The reply only says it: no error step, so it is no failure.
	case "quotatext":
		a.text("Error 429, Message: quota, Status: RESOURCE_EXHAUSTED")
		a.end(id, "end_turn")
	// A command that outlives the prompt: the response comes first, the command ends after FAKE_AGY_BG_MS, with a reply.
	case "bg":
		call := map[string]any{"toolCallId": "bg_1", "name": "run_command", "kind": "execute", "title": "sleep", "rawInput": map[string]any{"CommandLine": "sleep 1"}}
		a.text("started")
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call", "status": "in_progress"}))
		a.end(id, "end_turn")
		delay, _ := time.ParseDuration(os.Getenv("FAKE_AGY_BG_DELAY"))
		time.Sleep(delay)
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call_update", "status": "completed", "rawOutput": map[string]any{"output": "slept"}}))
		a.text("background done")
		a.update(map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100})
	// A late update of a session agy has left must not reach the turn.
	case "stale":
		a.send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "old-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "STALE"}}}})
		a.text("fresh")
		a.end(id, "end_turn")
	// A launch that completed whose child goes on, and a reply that comes after the turn ended.
	case "launch":
		call := map[string]any{"toolCallId": "call_9", "name": "invoke_subagent", "kind": "other", "title": "Invoke subagent", "rawInput": map[string]any{"Role": "r", "Prompt": "p"}}
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call", "status": "in_progress"}))
		a.update(merge(call, map[string]any{"sessionUpdate": "tool_call_update", "status": "completed"}))
		a.text("launched")
		a.end(id, "end_turn")
	case "late":
		a.text("ok")
		a.end(id, "end_turn")
		time.Sleep(400 * time.Millisecond)
		a.text("late reply")
		a.update(map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100})
	// A failure, then a reply the agent streams on its own afterwards.
	case "quotalate":
		a.step(sessiondb.StepError, "Error 429, Message: quota, Status: RESOURCE_EXHAUSTED")
		a.text("Error 429")
		a.end(id, "end_turn")
		time.Sleep(400 * time.Millisecond)
		a.text("detached reply")
		a.update(map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100})
	// A permission asked after the turn ended.
	case "permlate":
		a.text("ok")
		a.end(id, "end_turn")
		time.Sleep(400 * time.Millisecond)
		a.permission("late")
	// agy's own questions: a form, the legacy form and a question put as a permission.
	case "elicit":
		a.send(map[string]any{"id": "elicit-form", "method": "elicitation/create", "params": map[string]any{"mode": "form", "message": "q", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}})
		a.send(map[string]any{"id": "elicit-legacy", "method": "session/elicitation", "params": map[string]any{"mode": "form", "message": "q", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}})
		a.send(map[string]any{"id": "elicit-url", "method": "elicitation/create", "params": map[string]any{"mode": "url", "message": "q", "url": "https://example.com", "elicitationId": "e1"}})
		a.send(map[string]any{"id": "perm-question", "method": "session/request_permission", "params": map[string]any{"sessionId": a.session(), "toolCall": map[string]any{"toolCallId": "interaction_1", "title": "Pick"}, "options": []map[string]any{{"optionId": "a", "name": "A", "kind": "allow_once"}}}})
		a.text("asked")
		a.end(id, "end_turn")
	case "rejectprompt":
		a.fail(id, -32602, "invalid prompt parameters before accepting user input")
	case "rpcerror":
		a.fail(id, -32603, "boom")
	case "refusal":
		a.end(id, "refusal")
	case "maxtokens":
		a.text("cut")
		a.end(id, "max_tokens")
	case "crash":
		os.Exit(3)
	case "/compact":
		a.text("compacted")
		a.update(map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 1048576})
		a.end(id, "end_turn")
	default:
		a.text("ok")
		a.end(id, "end_turn")
	}
}

// failStep ends a turn as agy ends one on an upstream failure; reached: the model had answered before it.
func (a *agent) failStep(id json.RawMessage, text string, reached bool) {
	if reached {
		a.step(sessiondb.StepPlannerResponse, "answer")
	}
	a.step(sessiondb.StepError, text)
	a.text(text)
	a.end(id, "end_turn")
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
