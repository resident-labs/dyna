package harness

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/jsonrpc"
)

// Keep the existing Muse JSONL boundary so transcripts, tool tracking and
// quota observation continue to use the same decoder as saved exec output.
func museRecord(session, kind string, payload any) map[string]any {
	return map[string]any{"payload_type": kind, "stream": map[string]string{"kind": "session", "id": session}, "payload": payload}
}

type museItem struct {
	ID       string   `json:"itemId"`
	Turn     string   `json:"turnId"`
	Kind     string   `json:"kind"`
	Status   string   `json:"status"`
	Text     string   `json:"text"`
	Tool     string   `json:"tool"`
	Args     string   `json:"args"`
	Output   string   `json:"visibleOutput"`
	Summary  []string `json:"summary"`
	Revision int      `json:"revision"`
}
type museTurnNotifications struct {
	usage         map[string]int64
	session, turn string
	done          bool
	err           error
	items         map[string]museItem
	order         []string
}

func (n *museTurnNotifications) observe(u jsonrpc.Notification) []string {
	var p struct {
		Usage        map[string]int64 `json:"usage"`
		PromptTokens int64            `json:"promptTokens"`
		UsedTokens   int64            `json:"usedTokens"`
		Session      string           `json:"sessionId"`
		Turn         string           `json:"turnId"`
		Terminal     string           `json:"terminal"`
		Reason       string           `json:"reason"`
		Item         museItem         `json:"item"`
		ItemID       string           `json:"itemId"`
		Delta        string           `json:"delta"`
		Field        string           `json:"field"`
		Error        struct {
			Message string `json:"message"`
			Quota   *struct {
				ResetsAtMS int64 `json:"resetsAtMs"`
			} `json:"quota"`
		} `json:"error"`
	}
	if json.Unmarshal(u.Params, &p) != nil || (p.Session != "" && p.Session != n.session) || (p.Turn != "" && p.Turn != n.turn) || (p.Item.Turn != "" && p.Item.Turn != n.turn) {
		return nil
	}
	var lines []string
	emit := func(kind string, payload any) {
		b, _ := json.Marshal(museRecord(n.session, kind, payload))
		lines = append(lines, string(b))
	}
	switch u.Method {
	case "session/tokenUsage":
		if n.usage == nil {
			n.usage = map[string]int64{}
		}
		n.usage["input_tokens"] += p.PromptTokens
		n.usage["cached_input_tokens"] += p.Usage["cachedTokens"]
		n.usage["output_tokens"] += p.Usage["outputTokens"]
		emit("run.usage", map[string]any{})
	case "session/contextUsage":
		emit("run.context", map[string]any{"context_tokens": p.UsedTokens})
	case "turn/started":
		emit("run.lifecycle.started", map[string]any{})
	case "item/started", "item/updated", "item/completed":
		item := p.Item
		if item.ID == "" {
			return nil
		}
		if n.items == nil {
			n.items = map[string]museItem{}
		}
		old, exists := n.items[item.ID]
		if exists && item.Revision > 0 && item.Revision <= old.Revision {
			return nil
		}
		if !exists {
			n.order = append(n.order, item.ID)
		}
		n.items[item.ID] = item
		switch item.Kind {
		case "reasoning":
			text := item.Text
			if text == "" {
				text = strings.Join(item.Summary, "\n")
			}
			oldText := old.Text
			if oldText == "" {
				oldText = strings.Join(old.Summary, "\n")
			}
			emit("run.reasoning.delta", map[string]string{"text": strings.TrimPrefix(text, oldText)})
		case "agentMessage", "assistantMessage":
			delta := strings.TrimPrefix(item.Text, old.Text)
			if delta != "" {
				emit("run.output.delta", map[string]string{"text": delta})
			}
		case "toolCall", "userShell":
			tool := item.Tool
			if tool == "" {
				tool = "shell"
			}
			if !exists {
				emit("task.lifecycle.side_effect_intent", map[string]any{"event": map[string]string{"operation": "tool:" + tool, "idempotency_key": "tool:" + item.ID}})
			}
			if item.Status != "inProgress" && item.Status != "pending" && old.Status != item.Status {
				emit("tool.result", map[string]any{"call_id": item.ID, "text": item.Output, "correlation_facts": map[string]string{"tool_name": tool, "outcome": item.Status}})
			}
		}
	case "item/delta":
		item, ok := n.items[p.ItemID]
		if !ok {
			return nil
		}
		if p.Field == "" || p.Field == "text" {
			item.Text += p.Delta
			if item.Kind == "reasoning" {
				emit("run.reasoning.delta", map[string]string{"text": p.Delta})
			}
			if item.Kind == "agentMessage" || item.Kind == "assistantMessage" {
				emit("run.output.delta", map[string]string{"text": p.Delta})
			}
		} else if p.Field == "output" || p.Field == "visibleOutput" {
			item.Output += p.Delta
		}
		n.items[p.ItemID] = item
	case "turn/completed":
		n.done = true
		if n.usage == nil && p.Usage != nil {
			n.usage = map[string]int64{"input_tokens": p.Usage["inputTokens"], "cached_input_tokens": p.Usage["cachedTokens"], "output_tokens": p.Usage["outputTokens"]}
		}
		if p.Terminal == "completed" {
			var answer []string
			for _, id := range n.order {
				item := n.items[id]
				if (item.Kind == "agentMessage" || item.Kind == "assistantMessage") && item.Text != "" {
					answer = append(answer, item.Text)
				}
			}
			emit("run.terminal.completed", map[string]any{"text": strings.Join(answer, "\n\n"), "terminal": p.Terminal, "usage": n.usage})
		} else {
			detail := p.Error.Message
			if detail == "" {
				detail = p.Reason
			}
			if detail == "" {
				detail = p.Terminal
			}
			if p.Error.Quota != nil {
				detail = "429 Subscription quota exhausted. Your usage window resets at " + time.UnixMilli(p.Error.Quota.ResetsAtMS).UTC().Format(time.RFC3339Nano)
			}
			n.err = fmt.Errorf("Muse turn %s: %s", p.Terminal, detail)
			emit("run.terminal.failed", map[string]any{"text": detail, "terminal": p.Terminal, "usage": n.usage})
		}
	}
	return lines
}
