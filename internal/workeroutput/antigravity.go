package workeroutput

import (
	"encoding/json"
	"strconv"
	"strings"
)

// adaptAntigravityStreamJSON translates agy 1.2.x stream-json records.
func adaptAntigravityStreamJSON(record *antigravityRecord) ([]Event, bool) {
	switch record.Event {
	case "step_update":
		state := strings.ToUpper(strings.TrimSpace(record.StepUpdate.State))
		switch record.StepUpdate.StepType {
		case "tool":
			// ACTIVE still produces a boundary signal. The transcript presents the
			// call once, when DONE supplies its result or error.
			if state == "ACTIVE" {
				return nil, true
			}
			name := strings.TrimSpace(record.StepUpdate.ToolName)
			if name == "" {
				name = strings.TrimSpace(record.StepUpdate.ToolInfo.Name)
			}
			if name == "" {
				name = "tool"
			}
			input := compactJSON(record.StepUpdate.ToolInfo.Parameters)
			if name == "call_mcp_tool" {
				var call struct {
					Server    string          `json:"ServerName"`
					Tool      string          `json:"ToolName"`
					Arguments json.RawMessage `json:"Arguments"`
				}
				if json.Unmarshal(record.StepUpdate.ToolInfo.Parameters, &call) == nil && call.Server != "" && call.Tool != "" {
					name = strings.TrimPrefix(call.Server, "dyna-catalog_") + "__" + call.Tool
					input = compactJSON(call.Arguments)
				}
			}
			event := Event{Kind: "tool", Name: name, Input: input}
			switch state {
			case "DONE":
				event.State = "completed"
				event.Output = antigravityValue(record.StepUpdate.ToolInfo.Output)
			default:
				event.State = strings.ToLower(state)
				event.Output = antigravityValue(record.StepUpdate.ToolInfo.Output)
			}
			if record.StepUpdate.ToolInfo.Error != nil {
				event.State = "failed"
				event.Output = antigravityFailedToolOutput(event.Output, *record.StepUpdate.ToolInfo.Error)
			}
			return []Event{event}, true

		case "agent_response":
			var events []Event
			if record.StepUpdate.TextDelta != "" {
				event := Event{Kind: "message", Role: "assistant", State: "delta", Text: record.StepUpdate.TextDelta}
				if record.StepUpdate.Index != nil {
					event.ID = "response:" + strconv.Itoa(*record.StepUpdate.Index)
				}
				events = append(events, event)
			}
			if state == "DONE" {
				events = append(events, Event{Kind: "status", Name: "response", State: "completed"})
			}
			if len(events) > 0 {
				return events, true
			}

		case "user_input":
			if record.StepUpdate.TextDelta != "" {
				return []Event{{Kind: "message", Role: "user", Text: record.StepUpdate.TextDelta}}, true
			}
		}

	case "command_result":
		name := strings.TrimSpace(record.Command.Name)
		if name == "" {
			name = "command"
		}
		return []Event{{Kind: "status", Name: name, State: "completed", Text: compactJSON(record.Command.Data)}}, true

	case "result":
		state := strings.ToLower(strings.TrimSpace(record.Result.Status))
		if state == "" {
			state = "completed"
		}
		events := []Event{{Kind: "status", Name: "result", State: state}}
		if record.Result.Response != "" {
			events = append(events, Event{Kind: "message", Role: "assistant", Text: record.Result.Response})
		}
		return events, true
	}
	return nil, false
}

type antigravityError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func antigravityFailedToolOutput(output string, toolErr antigravityError) string {
	detail := strings.TrimSpace(toolErr.Message)
	if errorType := strings.TrimSpace(toolErr.Type); errorType != "" {
		if detail == "" {
			detail = errorType
		} else {
			detail = errorType + ": " + detail
		}
	}
	if output == "" {
		return detail
	}
	if detail == "" {
		return output
	}
	return strings.TrimRight(output, "\r\n") + "\n" + detail
}

// antigravityStreamState suppresses the terminal response only when it exactly
// repeats response text already emitted as step_update deltas. A result without
// any deltas remains the authoritative final answer.
type antigravityStreamState struct {
	response strings.Builder
}

func (s *antigravityStreamState) deduplicate(events []Event) []Event {
	result := false
	for _, event := range events {
		if event.Kind == "status" && event.Name == "result" {
			result = true
			break
		}
	}
	if !result {
		for _, event := range events {
			if event.Kind == "message" && event.Role == "assistant" {
				s.response.WriteString(event.Text)
			}
		}
		return events
	}

	streamed := s.response.String()
	s.response.Reset()
	if streamed == "" {
		return events
	}
	filtered := events[:0]
	for _, event := range events {
		if event.Kind == "message" && event.Role == "assistant" && event.Text == streamed {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func antigravityValue(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return compactJSON(raw)
}
