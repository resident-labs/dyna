package workeroutput

import (
	"encoding/json"
)

// DialectGrokJSON is Grok's --output-format streaming-json: type-tagged ACP
// updates with native text, thought, usage, end, and error envelopes.
const DialectGrokJSON = "grok-json"

type grokRecord struct {
	Type       string                     `json:"type"`
	Data       string                     `json:"data"`
	SessionID  string                     `json:"sessionId"`
	ToolCallID string                     `json:"toolCallId"`
	ToolName   string                     `json:"toolName"`
	Title      string                     `json:"title"`
	Kind       string                     `json:"kind"`
	Status     string                     `json:"status"`
	RawInput   json.RawMessage            `json:"rawInput"`
	RawOutput  json.RawMessage            `json:"rawOutput"`
	Content    json.RawMessage            `json:"content"`
	Message    string                     `json:"message"`
	StopReason string                     `json:"stopReason"`
	Usage      map[string]json.RawMessage `json:"usage"`
}

func decodeGrok(line []byte) Record {
	p, ok := parseRecord[grokRecord](line)
	if !ok {
		return Record{}
	}
	r := Record{valid: true, recognized: true, recordUsage: recordUsage{Type: p.Type}}
	switch p.Type {
	case "text":
		// Grok streams the reply a few tokens per record.
		r.Events = []Event{{Kind: "message", Role: "assistant", State: "delta", Text: p.Data}}
	case "thought":
		if p.Data != "" {
			r.Events = []Event{{Kind: "reasoning", State: "delta", Text: p.Data}}
		}
	case "tool_call", "tool_call_update":
		state := "updated"
		switch p.Status {
		case "pending", "in_progress":
			// tool_call_update records repeat in_progress without the tool
			// name; only the tool_call itself opens the tool.
			if p.Type == "tool_call" {
				state = "started"
				if p.ToolCallID != "" {
					r.Signals = []ToolSignal{{ID: p.ToolCallID, Start: true}}
				}
			}
		case "completed", "failed":
			state = p.Status
			if p.ToolCallID != "" {
				r.Signals = []ToolSignal{{ID: p.ToolCallID}}
			}
		}
		if p.Type == "tool_call" && p.Status == "" {
			state = "started"
			if p.ToolCallID != "" {
				r.Signals = []ToolSignal{{ID: p.ToolCallID, Start: true}}
			}
		}
		name := p.ToolName
		if name == "" {
			name = p.Title
		}
		if name == "" {
			name = p.Kind
		}
		input := compactJSON(p.RawInput)
		if p.ToolName == "use_tool" || p.Type == "tool_call_update" {
			var call struct {
				Name  string          `json:"tool_name"`
				Input json.RawMessage `json:"tool_input"`
			}
			if json.Unmarshal(p.RawInput, &call) == nil && call.Name != "" {
				name, input = call.Name, compactJSON(call.Input)
			}
		}
		output := grokToolOutput(p.RawOutput)
		if output == "" {
			output = compactJSON(p.Content)
		}
		r.Events = []Event{{Kind: "tool", ID: p.ToolCallID, Name: name, State: state, Input: input, Output: output}}
	case "usage":
		r.recordUsage.Usage = p.Usage
	case "end":
		r.SessionID = p.SessionID
		r.Events = []Event{{Kind: "status", Name: "turn", State: "completed"}}
	case "error":
		r.SessionID = p.SessionID
		r.Events = []Event{{Kind: "status", Name: "error", State: "failed", Text: p.Message}}
	// Grok 1.0.62 streaming-json names: a /compact run printed auto_compact_completed.
	// Grok compacts by itself at its threshold, so the trigger is auto.
	case "auto_compact_started":
		r.Events = []Event{{Kind: "status", Name: "compaction", State: "started", Trigger: "auto"}}
	case "auto_compact_completed":
		r.Compaction = true
		r.Events = []Event{{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto"}}
	case "auto_compact_failed", "auto_compact_cancelled":
		r.Events = []Event{{Kind: "status", Name: "compaction", State: "failed", Text: p.Message, Trigger: "auto"}}
	case "plan", "available_commands", "max_turns_reached":
		r.Events = []Event{{Kind: "status", Name: p.Type}}
	default:
		return Record{valid: true}
	}
	return r
}

// grokToolOutput exposes MCP text so shared tool consumers see the native result.
func grokToolOutput(raw json.RawMessage) string {
	var result struct {
		Type   string `json:"type"`
		Output struct {
			OkayOutput *string `json:"OkayOutput"`
			Error      *string `json:"Error"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &result) == nil && result.Type == "MCP" {
		if result.Output.OkayOutput != nil {
			return *result.Output.OkayOutput
		}
		if result.Output.Error != nil {
			return *result.Output.Error
		}
	}
	return compactJSON(raw)
}

func (n *UsageNormalizer) observeGrok(record recordUsage) (Usage, bool) {
	// usage records are per-response deltas. end repeats aggregate usage;
	// consuming both would count the same provider tokens twice.
	if record.Type != "usage" || record.Usage == nil {
		return Usage{}, false
	}
	read := func(key string, coverage *metricCoverage) int64 {
		var v int64
		raw, ok := record.Usage[key]
		if !ok || string(raw) == "null" || json.Unmarshal(raw, &v) != nil || v < 0 {
			coverage.missing = true
			return 0
		}
		coverage.observed = true
		return v
	}
	input := read("input_tokens", &n.input)
	cached := read("cache_read_input_tokens", &n.cached)
	write := read("cache_creation_input_tokens", &n.input)
	n.cost.missing = true
	return Usage{InputTokens: input + cached + write, CachedTokens: cached, OutputTokens: read("output_tokens", &n.output)}, true
}
