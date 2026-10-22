package harness

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/jsonrpc"
)

// Codex notifications are translated at the harness boundary. Existing output
// consumers continue to receive the Codex JSONL dialect and cumulative usage.
type codexTurnNotifications struct {
	thread, turn string
	usage        map[string]any
	done         bool
	err          error
}

func (n *codexTurnNotifications) observe(u jsonrpc.Notification) []string {
	var p struct {
		ThreadID string         `json:"threadId"`
		TurnID   string         `json:"turnId"`
		Item     map[string]any `json:"item"`
		Turn     struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"turn"`
		TokenUsage struct {
			Total struct {
				Input  int64 `json:"inputTokens"`
				Cached int64 `json:"cachedInputTokens"`
				Output int64 `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(u.Params, &p) != nil || (p.ThreadID != "" && p.ThreadID != n.thread) || (p.TurnID != "" && p.TurnID != n.turn) {
		return nil
	}
	record := map[string]any{}
	switch u.Method {
	case "turn/started":
		if p.Turn.ID != n.turn {
			return nil
		}
		record["type"] = "turn.started"
	case "thread/tokenUsage/updated":
		n.usage = map[string]any{"input_tokens": p.TokenUsage.Total.Input, "cached_input_tokens": p.TokenUsage.Total.Cached, "output_tokens": p.TokenUsage.Total.Output}
		return nil
	case "turn/completed":
		if p.Turn.ID != n.turn {
			return nil
		}
		n.done = true
		if p.Turn.Status == "completed" {
			record["type"] = "turn.completed"
			if n.usage != nil {
				record["usage"] = n.usage
			}
		} else {
			detail := strings.TrimSpace(string(p.Turn.Error))
			if detail == "" || detail == "null" {
				detail = p.Turn.Status
			}
			n.err = fmt.Errorf("Codex turn %s: %s", p.Turn.Status, detail)
			record["type"] = "turn.failed"
			record["error"] = map[string]any{"message": n.err.Error()}
		}
	case "item/started", "item/completed":
		item := p.Item
		if item == nil {
			return nil
		}
		switch item["type"] {
		case "agentMessage":
			item["type"] = "agent_message"
		case "commandExecution":
			item["type"] = "command_execution"
			item["aggregated_output"] = item["aggregatedOutput"]
			delete(item, "aggregatedOutput")
			item["exit_code"] = item["exitCode"]
			delete(item, "exitCode")
		case "mcpToolCall":
			item["type"] = "mcp_tool_call"
		case "dynamicToolCall":
			item["type"] = "mcp_tool_call"
			item["server"] = item["namespace"]
			item["result"] = item["contentItems"]
		case "fileChange":
			item["type"] = "file_change"
			if changes, ok := item["changes"].([]any); ok {
				for _, raw := range changes {
					if change, ok := raw.(map[string]any); ok {
						if kind, ok := change["kind"].(map[string]any); ok {
							change["kind"] = kind["type"]
						}
					}
				}
			}
		case "webSearch":
			item["type"] = "web_search"
		case "reasoning":
			var text []string
			for _, key := range []string{"summary", "content"} {
				if parts, ok := item[key].([]any); ok {
					for _, part := range parts {
						if value, ok := part.(string); ok {
							text = append(text, value)
						}
					}
				}
			}
			item["text"] = strings.Join(text, "\n")
		case "contextCompaction":
			// The decoder reads the item's start and completion as compaction.
		default:
			return nil
		}
		if record["type"] == nil {
			record["type"] = "item." + strings.TrimPrefix(u.Method, "item/")
			record["item"] = item
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		record["type"] = "item.updated"
		record["item"] = map[string]any{"id": p.ItemID, "type": "reasoning", "text": p.Delta}
	default:
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil
	}
	return []string{string(data)}
}
