package workeroutput

import (
	"encoding/json"
	"regexp"
	"strings"
)

// DialectMuseJSONL is `muse exec --json`: one MSP (Muse session protocol)
// JSON record per stdout line. Records carry a `payload_type` and a typed
// `payload`; the final answer is the `run.terminal.completed` payload's text,
// streamed beforehand as `run.output.delta` records. The bundled Muse
// profiles (wonder, wonder-full) use it.
const DialectMuseJSONL = "muse-jsonl"

type museRecord struct {
	PayloadType string `json:"payload_type"`
	Stream      struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"stream"`
	Payload musePayload `json:"payload"`
}

func adaptMuseJSONL(record *museRecord) ([]Event, bool) {
	payload := record.Payload

	switch record.PayloadType {
	case "turn.input.user":
		if strings.TrimSpace(payload.Prompt) != "" {
			return []Event{{Kind: "message", Role: "user", Text: payload.Prompt}}, true
		}
		return []Event{}, true
	case "run.reasoning.delta":
		return []Event{{Kind: "reasoning", Text: payload.Text, State: "delta"}}, true
	case "run.usage", "run.context":
		return []Event{}, true
	case "run.output.delta":
		if payload.Text != "" {
			return []Event{{Kind: "message", Role: "assistant", State: "delta", Text: payload.Text}}, true
		}
		return []Event{}, true
	case "run.terminal.completed":
		return []Event{{Kind: "status", Name: "result", State: "completed"}}, true
	case "run.terminal.failed":
		return []Event{{Kind: "status", Name: "result", State: "failed"}}, true
	case "run.lifecycle.started":
		return []Event{{Kind: "status", Name: "run", State: "started"}}, true
	case "run.model.configured":
		return []Event{{Kind: "status", Name: "model", State: "configured"}}, true
	case "runtime.command.accepted":
		return []Event{{Kind: "status", Name: "command", State: "accepted"}}, true
	case "session.run.linked":
		return []Event{{Kind: "status", Name: "session", State: "linked"}}, true
	case "task.stream.linked":
		return []Event{{Kind: "status", Name: "task", State: "linked"}}, true
	case "task.lifecycle.proposed":
		// A tool task is only proposed here, keyed by its task id. The call
		// starts at side_effect_intent, the first record that carries the
		// call id `tool.result` later reports, so starts and results pair.
		if strings.HasPrefix(payload.Event.TaskKind, "tool.") {
			return []Event{}, true
		}
		return []Event{{Kind: "status", Name: "task", State: payload.Event.Kind}}, true
	case "task.lifecycle.side_effect_intent":
		name, okName := strings.CutPrefix(payload.Event.Operation, "tool:")
		callID, okID := strings.CutPrefix(payload.Event.IdempotencyKey, "tool:")
		if okName && okID && name != "" && callID != "" {
			return []Event{{Kind: "tool", Name: name, State: "started", ID: callID}}, true
		}
		return []Event{}, true
	case "task.lifecycle.output", "task.lifecycle.tool_output_ref":
		// Chunked tool output and output sidecar refs; the consolidated
		// `tool.result` record follows.
		return []Event{}, true
	case "task.lifecycle.accepted", "task.lifecycle.scheduled", "task.lifecycle.started",
		"task.lifecycle.status", "task.lifecycle.completed", "task.lifecycle.failed",
		"task.lifecycle.rejected", "task.lifecycle.cancelled", "task.lifecycle.canceled":
		return []Event{{Kind: "status", Name: "task", State: payload.Event.Kind}}, true
	case "tool.result":
		name := payload.CorrelationFacts.ToolName
		if name == "" {
			name = "tool"
		}
		state := "completed"
		if payload.CorrelationFacts.Outcome == "failure" || payload.CorrelationFacts.Outcome == "failed" {
			state = "failed"
		}
		input, output, exit, shell := museResultParts(name, payload.Text)
		if name == "tool" && shell {
			// Some shell results (long-running command polls) report no
			// tool name; the envelope gives them away.
			name = "bash"
		}
		return []Event{{Kind: "tool", Name: name, State: state, ID: payload.CallID, Input: input, Output: output, Exit: exit}}, true
	}
	return nil, false
}

// Muse's stream never reports a tool call's input on its own record; what
// the call did only echoes back inside the result text each tool returns.
// museResultParts recovers the input (and, for shell calls, the exit code
// and the bare command output) from that text, in the shapes the other
// dialects produce, so readers summarize muse calls like any other.
func museResultParts(name, text string) (input, output string, exit *int, shell bool) {
	output = text
	// Shell tools return a JSON envelope that repeats the command. A
	// finished command carries terminal_status; a chunk of one still
	// running carries execution_state instead. Both carry chunk_id.
	var envelope struct {
		Command        string `json:"command"`
		Description    string `json:"description"`
		ExitCode       *int   `json:"exit_code"`
		TerminalStatus string `json:"terminal_status"`
		ExecutionState string `json:"execution_state"`
		ChunkID        string `json:"chunk_id"`
		Output         string `json:"output"`
	}
	if json.Unmarshal([]byte(text), &envelope) == nil && envelope.Command != "" &&
		(envelope.TerminalStatus != "" || envelope.ExecutionState != "" || envelope.ChunkID != "") {
		output, exit = envelope.Output, envelope.ExitCode
		if envelope.Command != "" {
			args := struct {
				Command     string `json:"command"`
				Description string `json:"description,omitempty"`
			}{envelope.Command, envelope.Description}
			if b, err := json.Marshal(args); err == nil {
				input = string(b)
			}
		}
		return input, output, exit, true
	}
	var path string
	switch name {
	case "read_file":
		// "Read text file `path`.\n<contents>"
		if m := museReadPath.FindStringSubmatch(text); m != nil {
			path = m[1]
		}
	case "write_file":
		// "wrote 12 bytes to path; note: …"
		if m := museWrotePath.FindStringSubmatch(text); m != nil {
			path = strings.TrimSpace(m[1])
		}
	}
	if path != "" {
		if b, err := json.Marshal(struct {
			Path string `json:"path"`
		}{path}); err == nil {
			input = string(b)
		}
	}
	return input, output, exit, false
}

var (
	museReadPath  = regexp.MustCompile("^Read [^`\n]*`([^`]+)`")
	museWrotePath = regexp.MustCompile(`(?m)^wrote [0-9]+ bytes to ([^;\n]+)`)
)

func museToolSignals(record *museRecord) []ToolSignal {
	switch record.PayloadType {
	case "task.lifecycle.side_effect_intent":
		if !strings.HasPrefix(record.Payload.Event.Operation, "tool:") {
			return nil
		}
		id, ok := strings.CutPrefix(record.Payload.Event.IdempotencyKey, "tool:")
		if !ok || id == "" {
			return nil
		}
		return []ToolSignal{{ID: id, Start: true}}
	case "tool.result":
		if record.Payload.CallID == "" {
			return nil
		}
		return []ToolSignal{{ID: record.Payload.CallID}}
	}
	return nil
}

// MuseStreamResult extracts the final response and session id from a
// `muse exec --json` stdout capture: the `run.terminal.completed` payload's
// text wins; otherwise the streamed `run.output.delta` texts are joined. The
// session id is the records' session stream id.
func MuseStreamResult(stdout string) (result, sessionID string) {
	var deltas []string
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[museRecord]([]byte(line))
		if !ok {
			continue
		}
		if record.Stream.Kind == "session" && record.Stream.ID != "" && sessionID == "" {
			sessionID = record.Stream.ID
		}
		switch record.PayloadType {
		case "run.output.delta":
			if record.Payload.Text != "" {
				deltas = append(deltas, record.Payload.Text)
			}
		case "run.terminal.completed":
			if strings.TrimSpace(record.Payload.Text) != "" {
				result = strings.TrimSpace(record.Payload.Text)
			}
		}
	}
	if result == "" && len(deltas) > 0 {
		result = strings.TrimSpace(strings.Join(deltas, ""))
	}
	return result, sessionID
}

// UpgradeLegacyEvent recovers fields omitted by older normalized artifacts.
// The Muse result decoder owns these envelopes for both old and live records.
// TODO: legacy/shim remove later — fills omitted fields in old normalized Muse artifacts; remove after affected artifacts expire or migrate
func UpgradeLegacyEvent(e Event) Event {
	if e.Kind != "tool" {
		return e
	}
	name := strings.ToLower(e.Name)
	var args map[string]any
	_ = json.Unmarshal([]byte(e.Input), &args)
	has := func(keys ...string) bool {
		for _, key := range keys {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	switch name {
	case "command", "bash", "shell", "run_command", "run_terminal_cmd":
		if strings.TrimSpace(e.Input) != "" {
			return e
		}
	case "tool", "bash_input":
		if has("command") {
			return e
		}
	case "read", "read_file", "view_file":
		name = "read_file"
		if has("file_path", "path", "target_file", "AbsolutePath") {
			return e
		}
	case "write", "write_file", "write_to_file":
		name = "write_file"
		if has("file_path", "path", "target_file", "TargetFile") {
			return e
		}
	default:
		return e
	}
	input, output, exit, shell := museResultParts(name, e.Output)
	if input == "" {
		return e
	}
	e.Input = input
	if shell {
		e.Output = output
		if e.Exit == nil {
			e.Exit = exit
		}
	}
	return e
}

type musePayload struct {
	Usage         map[string]json.RawMessage `json:"usage"`
	ContextTokens int64                      `json:"context_tokens"`
	raw           json.RawMessage
	Prompt        string `json:"prompt"`
	Text          string `json:"text"`
	Terminal      string `json:"terminal"`
	CallID        string `json:"call_id"`
	Event         struct {
		Kind           string `json:"kind"`
		TaskKind       string `json:"task_kind"`
		TaskID         string `json:"task_id"`
		Operation      string `json:"operation"`
		IdempotencyKey string `json:"idempotency_key"`
	} `json:"event"`
	CorrelationFacts struct {
		ToolName string `json:"tool_name"`
		Outcome  string `json:"outcome"`
	} `json:"correlation_facts"`
}

func (p *musePayload) UnmarshalJSON(raw []byte) error {
	type fields musePayload
	var decoded fields
	_ = json.Unmarshal(raw, &decoded)
	*p = musePayload(decoded)
	p.raw = append(json.RawMessage(nil), raw...)
	return nil
}
