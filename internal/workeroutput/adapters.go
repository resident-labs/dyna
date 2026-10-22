package workeroutput

import (
	"encoding/json"
	"strings"
)

func adaptCodexJSONL(record *codexRecord) ([]Event, bool) {
	switch record.Type {
	case "thread.started":
		return []Event{{Kind: "status", Name: "thread", State: "started"}}, true
	case "turn.started":
		return []Event{{Kind: "status", Name: "turn", State: "started"}}, true
	case "turn.completed":
		return []Event{{Kind: "status", Name: "turn", State: "completed"}}, true
	case "turn.failed":
		return []Event{{Kind: "status", Name: "turn", State: "failed", Text: record.Error.Message}}, true
	case "error":
		text := record.Message
		if text == "" {
			text = record.Error.Message
		}
		return []Event{{Kind: "status", Name: "error", State: "failed", Text: text}}, true
	case "item.started", "item.completed", "item.updated":
		state := strings.TrimPrefix(record.Type, "item.")
		switch record.Item.Type {
		case "agent_message":
			if state == "completed" && record.Item.Text != "" {
				return []Event{{Kind: "message", Role: "assistant", Text: record.Item.Text}}, true
			}
		case "reasoning":
			if record.Item.Text != "" {
				return []Event{{Kind: "reasoning", State: state, Text: record.Item.Text}}, true
			}
		case "command_execution":
			if record.Item.Command != "" {
				ev := Event{
					Kind: "tool", Name: "command", State: state, ID: record.Item.ID,
					Input: record.Item.Command, Output: record.Item.AggregatedOutput,
				}
				if state == "completed" {
					ev.Exit = record.Item.ExitCode
					if record.Item.Status == "failed" || (ev.Exit != nil && *ev.Exit != 0) {
						ev.State = "failed"
					}
				}
				return []Event{ev}, true
			}
		case "file_change":
			var changes []string
			for _, change := range record.Item.Changes {
				text := strings.TrimSpace(strings.TrimSpace(change.Kind) + " " + strings.TrimSpace(change.Path))
				if text != "" {
					changes = append(changes, text)
				}
			}
			return []Event{{Kind: "tool", Name: "file_change", State: state, ID: record.Item.ID, Input: strings.Join(changes, "\n")}}, true
		case "mcp_tool_call":
			name := strings.Trim(strings.TrimSpace(record.Item.Server)+"/"+strings.TrimSpace(record.Item.Tool), "/")
			if name == "" {
				name = "mcp"
			}
			input := compactJSON(record.Item.Arguments)
			if input == "" {
				input = record.Item.Tool
			}
			return []Event{{
				Kind: "tool", Name: name, State: state, ID: record.Item.ID,
				Input: input, Output: compactJSON(record.Item.Result),
			}}, true
		case "web_search":
			if record.Item.Query != "" {
				return []Event{{Kind: "tool", Name: "web_search", State: state, ID: record.Item.ID, Input: record.Item.Query}}, true
			}
			if state == "started" {
				return []Event{{Kind: "tool", Name: "web_search", State: state, ID: record.Item.ID}}, true
			}
		case "error":
			text := record.Item.Message
			if text == "" {
				text = record.Item.Text
			}
			if text != "" {
				return []Event{{Kind: "status", Name: "error", State: state, Text: text}}, true
			}
		// Codex names the compaction item in snake_case on the exec stream and
		// in camelCase on the app-server protocol.
		case "context_compaction", "contextCompaction":
			switch state {
			case "started":
				return []Event{{Kind: "status", Name: "compaction", State: "started", Trigger: "auto"}}, true
			case "completed":
				return []Event{{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto"}}, true
			}
		}
	}
	return nil, false
}

func adaptOpenCodeJSON(record *openCodeRecord) ([]Event, bool) {
	switch record.Type {
	case "text":
		return []Event{{Kind: "message", Role: "assistant", Text: record.Part.Text}}, true
	case "reasoning":
		// Redacted reasoning arrives with empty text.
		if strings.TrimSpace(record.Part.Text) == "" {
			return nil, true
		}
		return []Event{{Kind: "reasoning", State: "completed", Text: record.Part.Text}}, true
	case "step_start":
		return []Event{{Kind: "status", Name: "step", State: "started"}}, true
	case "step_finish":
		return []Event{{Kind: "status", Name: "step", State: "completed", Text: record.Part.Reason}}, true
	case "tool_use":
		id := record.Part.CallID
		if id == "" {
			id = record.Part.ID
		}
		state := record.Part.State.Status
		switch state {
		case "error":
			state = "failed"
		case "pending":
			state = "started"
		case "running":
			state = "updated"
		}
		output := openCodeSummary(record.Part.State.Output)
		if record.Part.State.Error != "" {
			output = record.Part.State.Error
		}
		return []Event{{Kind: "tool", ID: id, Name: record.Part.Tool, State: state,
			Input: openCodeSummary(record.Part.State.Input), Output: output}}, true
	case "error":
		return []Event{{Kind: "status", Name: "error", State: "failed", Text: openCodeSummary(record.Error)}}, true
	}
	return nil, false
}

// OpenCode tool output and errors can be text or structured JSON.
func openCodeSummary(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return compactJSON(raw)
}
