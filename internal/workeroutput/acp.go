package workeroutput

import (
	"encoding/json"
	"strings"
)

// DialectAcpJSON is ACP session traffic: JSON-RPC notifications from an agent
// Dyna drives over stdio (Grok `agent stdio`; Kimi Code's ACP server). The
// records are grok's streaming-json shapes wrapped in JSON-RPC envelopes, so
// the decoder unwraps and reuses the grok record decoding.
const DialectAcpJSON = "acp-json"

// acpNotification is one JSON-RPC notification line.
type acpNotification struct {
	Method string `json:"method"`
	Params struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	} `json:"params"`
}

// acpUpdateKind reads a notification's sessionUpdate kind plus the fields the
// grok record decoder shares.
type acpUpdate struct {
	SessionUpdate string                     `json:"sessionUpdate"`
	Content       json.RawMessage            `json:"content"`
	Status        string                     `json:"status"`
	StopReason    string                     `json:"stop_reason"`
	Usage         map[string]json.RawMessage `json:"usage"`
	Used          int64                      `json:"used"`
}

// contentText extracts a single text block's text; message chunks carry an
// object while tool calls carry an array.
func contentText(raw json.RawMessage) string {
	var block struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &block) != nil {
		return ""
	}
	return block.Text
}

func decodeAcp(line []byte) Record {
	n, ok := parseRecord[acpNotification](line)
	if !ok || n == nil {
		return Record{}
	}
	var update acpUpdate
	if json.Unmarshal(n.Params.Update, &update) != nil {
		return Record{valid: true}
	}
	var record grokRecord
	_ = json.Unmarshal(n.Params.Update, &record)
	// ACP names the finished status "successful"; grok's own stream says
	// "completed". Both decode the same way after this rewrite.
	if record.Status == "successful" {
		record.Status = "completed"
	}
	switch n.Method {
	case "session/update", "x.ai/session_notification", "_x.ai/session_notification", "_x.ai/session/update":
	default:
		return Record{valid: true}
	}
	switch update.SessionUpdate {
	case "agent_message_chunk":
		record.Type, record.Data = "text", contentText(update.Content)
	case "agent_thought_chunk":
		record.Type, record.Data = "thought", contentText(update.Content)
	case "tool_call", "tool_call_update":
		record.Type = update.SessionUpdate
	case "user_message_chunk":
		return Record{valid: true}
	case "available_commands_update":
		record.Type = "available_commands"
	case "plan":
		record.Type = "plan"
	case "compaction":
		record.Type = "auto_compact_completed"
		switch update.Status {
		case "in_progress":
			record.Type = "auto_compact_started"
		case "failed", "cancelled":
			record.Type = "auto_compact_failed"
		}
	case "usage_update":
		// ACP reports current context occupancy, not billable token deltas.
		used := update.Used
		if used < 0 {
			used = 0
		}
		return Record{ContextTokens: used, valid: true, recognized: true}
	case "response_completed":
		record.Type, record.Usage = "usage", update.Usage
	case "turn_completed":
		record.Type, record.SessionID = "end", n.Params.SessionID
		record.StopReason = update.StopReason
	case "model_changed", "session_info_update", "current_mode_update", "config_option_update", "available_commands":
		return Record{valid: true}
	default:
		return Record{valid: true}
	}
	return decodeGrok(mustMarshalRecord(record))
}

// mustMarshalRecord re-encodes the unwrapped update as a grok streaming-json
// record for the shared decoder.
func mustMarshalRecord(r grokRecord) []byte {
	raw, err := json.Marshal(r)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// AcpOutput collects the assistant text of each prompt by its id. The turn's
// answer belongs to the prompt whose reply settles last, so late chunks of an
// older queued prompt stay in their own buffer and never pollute it.
func AcpOutput() *acpAccumulator { return &acpAccumulator{} }

type acpAccumulator struct {
	answers  map[string]*strings.Builder
	fallback strings.Builder
}

// Observe records one notification frame's assistant text.
func (a *acpAccumulator) Observe(line string) {
	var envelope struct {
		Params struct {
			Meta struct {
				PromptID string `json:"promptId"`
			} `json:"_meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal([]byte(line), &envelope)
	id := envelope.Params.Meta.PromptID
	target := &a.fallback
	if id != "" {
		if a.answers == nil {
			a.answers = map[string]*strings.Builder{}
		}
		if a.answers[id] == nil {
			a.answers[id] = &strings.Builder{}
		}
		target = a.answers[id]
	}
	r := decodeAcp([]byte(line))
	for _, e := range r.Events {
		if e.Kind == "message" && e.Role == "assistant" && e.State == "delta" {
			target.WriteString(e.Text)
		}
	}
}

// Take returns the answer of the named prompt; prompts without an id share
// one answer.
func (a *acpAccumulator) Take(promptID string) string {
	if promptID != "" && a.answers[promptID] != nil {
		return strings.TrimSpace(a.answers[promptID].String())
	}
	return strings.TrimSpace(a.fallback.String())
}

// Reset starts a fresh accumulation (a new prompt).
func (a *acpAccumulator) Reset() {
	a.answers = nil
	a.fallback.Reset()
}
