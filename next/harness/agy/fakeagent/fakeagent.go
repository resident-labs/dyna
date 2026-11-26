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
//	tos            a TOS_VIOLATION as the chat text
//	rpcerror       a JSON-RPC error answer
//	refusal        stopReason refusal
//	maxtokens      stopReason max_tokens
//	crash          exit at once
//	/compact       reply "compacted"
//
// Environment: FAKE_AGY_LOG (file every received line is appended to, the answers to its permission requests included),
// FAKE_AGY_VERSION, FAKE_AGY_NO_APPEND, FAKE_AGY_AUTH_FAIL, FAKE_AGY_MISSING (a session id resume refuses),
// FAKE_AGY_STARTUP_PERMISSION (ask permission once the session exists, with no prompt open), FAKE_AGY_NO_COMPACT.
package fakeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// EnvVar is what a test sets in the re-executed process so its TestMain calls Run.
const EnvVar = "AGY_FAKE_AGENT"

type agent struct {
	out    io.Writer
	writeM sync.Mutex
	logM   sync.Mutex
	log    *os.File

	mu       sync.Mutex
	sessions int
	model    string
	thought  string
	held     chan string // the running "hold" prompt waits on this: "cancel" or "steer"
}

// Run serves the agent protocol on in and out until in ends.
func Run(in io.Reader, out io.Writer) {
	a := &agent{out: out, model: "gemini-4-neon", thought: "high"}
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

func (a *agent) update(update map[string]any) {
	a.send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "x", "update": update}})
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
		version := os.Getenv("FAKE_AGY_VERSION")
		if version == "" {
			version = "1.5.18"
		}
		meta := map[string]any{"antigravity": map[string]any{"tier": true, "systemPrompt": map[string]any{"append": os.Getenv("FAKE_AGY_NO_APPEND") == ""}}}
		a.reply(id, map[string]any{
			"protocolVersion":   1,
			"agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"image": true}, "sessionCapabilities": map[string]any{"resume": map[string]any{}}, "_meta": meta},
			"agentInfo":         map[string]any{"name": "Antigravity ACP Server", "version": version},
			"authMethods":       []map[string]any{{"id": "oauth-personal", "name": "Log in with Google"}},
		})
	case "authenticate":
		if os.Getenv("FAKE_AGY_AUTH_FAIL") != "" {
			a.fail(id, -32603, "No browser is available here to sign in")
			return
		}
		a.reply(id, map[string]any{"_meta": map[string]any{"antigravity": map[string]any{"tier": map[string]any{"name": "fake"}}}})
	case "session/new", "session/resume":
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
		a.mu.Unlock()
		result := map[string]any{"configOptions": a.options()}
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
	case "session/set_config_option":
		var p struct{ ConfigID, Value string }
		json.Unmarshal(raw, &p)
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
		go a.prompt(id, raw)
	default:
		if len(id) > 0 {
			a.fail(id, -32601, "method not found")
		}
	}
}

// permission asks as agy does for a shell command and logs the answer.
func (a *agent) permission(title string) {
	a.send(map[string]any{"id": "perm-" + title, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "x",
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

func (a *agent) prompt(id json.RawMessage, raw json.RawMessage) {
	var p struct {
		Prompt []struct{ Text string } `json:"prompt"`
	}
	json.Unmarshal(raw, &p)
	text := ""
	if len(p.Prompt) > 0 {
		text = p.Prompt[len(p.Prompt)-1].Text
	}
	a.mu.Lock()
	if a.held != nil && text != "stubborn" {
		// A prompt while another runs steers it.
		held := a.held
		a.mu.Unlock()
		a.text("steer seen")
		a.end(id, "end_turn")
		held <- "steer"
		return
	}
	a.mu.Unlock()
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
	case "hold", "stubborn":
		held := make(chan string, 4)
		a.mu.Lock()
		a.held = held
		a.mu.Unlock()
		a.text("working")
		if verb == "stubborn" {
			select {}
		}
		reason := <-held
		a.mu.Lock()
		a.held = nil
		a.mu.Unlock()
		if reason == "cancel" {
			a.end(id, "cancelled")
			return
		}
		a.text("after steer")
		a.end(id, "end_turn")
	case "quota":
		a.text("Error 429, Message: You have exhausted your capacity on this model. Your quota will reset after 4m42s. Resets in 4m42s., Status: RESOURCE_EXHAUSTED")
		a.end(id, "end_turn")
	case "forbidden":
		a.text("Error 403, Message: The caller does not have permission, Status: PERMISSION_DENIED")
		a.end(id, "end_turn")
	case "tos":
		a.text("Your account is restricted: TOS_VIOLATION")
		a.end(id, "end_turn")
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
