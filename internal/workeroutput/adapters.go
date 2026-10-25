package workeroutput

import (
	"encoding/json"
	"strings"
)

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
