package harness

import (
	"encoding/json"
	"fmt"
	"time"

	"dyna-agent/internal/providerfailure"
)

// Translate v2 events into the existing OpenCode CLI JSONL contract. Quota,
// transcript, usage and tool tracking continue to share one decoder.
type openCodeTurnNotifications struct {
	session      string
	pending      map[string]bool
	heldTerminal bool
	tools        map[string]openCodeTool
	done         bool
	err          error
}

type openCodeTool struct {
	name  string
	input json.RawMessage
}

func (n *openCodeTurnNotifications) observe(event openCodeEvent) []string {
	var d struct {
		Session string          `json:"sessionID"`
		Inbox   string          `json:"inboxID"`
		Message string          `json:"assistantMessageID"`
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Text    string          `json:"text"`
		Finish  string          `json:"finish"`
		Cost    *float64        `json:"cost"`
		Tokens  json.RawMessage `json:"tokens"`
		Input   json.RawMessage `json:"input"`
		Content json.RawMessage `json:"content"`
		Error   struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(event.Data, &d) != nil || d.Session != n.session {
		return nil
	}
	part := map[string]any{"id": d.ID, "messageID": d.Message, "sessionID": n.session}
	kind := ""
	switch event.Type {
	case "session.execution.started":
		n.done = false
		n.heldTerminal = false
	case "session.inbox.delivered", "session.inbox.cancelled":
		delete(n.pending, d.Inbox)
		if event.Type == "session.inbox.cancelled" && n.heldTerminal && len(n.pending) == 0 {
			n.done = true
		}
	case "session.step.started":
		kind = "step_start"
		part["type"] = "step-start"
	case "session.step.ended", "session.step.failed":
		kind = "step_finish"
		part["type"] = "step-finish"
		part["reason"] = d.Finish
		if len(d.Tokens) > 0 {
			part["tokens"] = d.Tokens
		}
		if d.Cost != nil {
			part["cost"] = d.Cost
		}
	case "session.text.ended":
		kind = "text"
		part["type"] = "text"
		part["text"] = d.Text
	case "session.reasoning.ended":
		kind = "reasoning"
		part["type"] = "reasoning"
		part["text"] = d.Text
	case "session.tool.input.started":
		if n.tools == nil {
			n.tools = map[string]openCodeTool{}
		}
		n.tools[d.ID] = openCodeTool{name: d.Name}
	case "session.tool.called":
		if n.tools == nil {
			n.tools = map[string]openCodeTool{}
		}
		tool := n.tools[d.ID]
		tool.input = d.Input
		n.tools[d.ID] = tool
	case "session.tool.success", "session.tool.failed":
		if tool, ok := n.tools[d.ID]; ok {
			d.Name = tool.name
			d.Input = tool.input
			delete(n.tools, d.ID)
		}
		kind = "tool_use"
		part["type"] = "tool"
		part["tool"] = d.Name
		status := "completed"
		if event.Type == "session.tool.failed" {
			status = "error"
		}
		part["state"] = map[string]any{"status": status, "input": d.Input, "output": d.Content, "error": d.Error.Message}
	case "session.execution.succeeded":
		n.heldTerminal = true
		n.done = len(n.pending) == 0
	case "session.execution.interrupted":
		n.done = true
		n.err = fmt.Errorf("OpenCode turn interrupted")
	case "session.execution.failed":
		n.done = true
		detail := d.Error.Message
		// The provider's own status and type name the reason; its text is only the
		// fallback. A 402 asks for payment, so it names no provider failure.
		reason, _ := providerfailure.StatusReason(d.Error.Status)
		if reason == "" {
			reason, _ = providerfailure.ClassifyField(d.Error.Type)
		}
		if d.Error.Status == 402 {
			reason = ""
		}
		if reason == providerfailure.Quota {
			detail = "429 " + detail
		}
		n.err = providerfailure.Tag(reason, fmt.Errorf("OpenCode turn failed: %s", detail))
		raw, _ := json.Marshal(map[string]any{"type": "error", "sessionID": n.session, "error": map[string]any{"name": d.Error.Type, "data": map[string]any{"message": detail}}})
		return []string{string(raw)}
	}
	if kind == "" {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"type": kind, "sessionID": n.session, "timestamp": time.Now().UnixMilli(), "part": part})
	return []string{string(raw)}
}
