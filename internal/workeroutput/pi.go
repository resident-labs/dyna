package workeroutput

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/providerfailure"
)

// DialectPiJSON is @earendil-works/pi-coding-agent --print --mode json.
const DialectPiJSON = "pi-json"

type piMessage struct {
	Role         string                     `json:"role"`
	Content      json.RawMessage            `json:"content"`
	StopReason   string                     `json:"stopReason"`
	ErrorMessage string                     `json:"errorMessage"`
	Usage        map[string]json.RawMessage `json:"usage"`
}

type piRecord struct {
	Type                  string          `json:"type"`
	ID                    string          `json:"id"`
	ToolCallID            string          `json:"toolCallId"`
	ToolName              string          `json:"toolName"`
	Args                  json.RawMessage `json:"args"`
	Result                json.RawMessage `json:"result"`
	PartialResult         json.RawMessage `json:"partialResult"`
	IsError               bool            `json:"isError"`
	Aborted               bool            `json:"aborted"`
	ErrorMessage          string          `json:"errorMessage"`
	Reason                string          `json:"reason"`
	Message               piMessage       `json:"message"`
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

type piBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

func piBlocks(raw json.RawMessage) []piBlock {
	var blocks []piBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

func piText(raw json.RawMessage) string {
	var parts []string
	for _, b := range piBlocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// piResultText keeps a tool result's text blocks as the event output, so a
// Dyna shell result reaches the transcript as its "Job sh_..." line instead
// of the JSON envelope. Results without text blocks keep the compact JSON.
func piResultText(raw json.RawMessage) string {
	var envelope struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Content) > 0 {
		if text := piText(envelope.Content); text != "" {
			return text
		}
	}
	return compactJSON(raw)
}

// piEvents keeps the message's block order, so a thought stays before the
// reply it led to. Adjacent text blocks form one message.
func piEvents(raw json.RawMessage) []Event {
	var events []Event
	for _, b := range piBlocks(raw) {
		switch {
		case b.Type == "text" && b.Text != "":
			if n := len(events); n > 0 && events[n-1].Kind == "message" {
				events[n-1].Text += "\n" + b.Text
				continue
			}
			events = append(events, Event{Kind: "message", Role: "assistant", Text: b.Text})
		case b.Type == "thinking" && strings.TrimSpace(b.Thinking) != "":
			events = append(events, Event{Kind: "reasoning", State: "completed", Text: b.Thinking})
		}
	}
	return events
}

// piCompactionResult is the token accounting of a successful compaction
// (docs/json.md). Pi names the size after compaction as an estimate.
func piCompactionResult(raw json.RawMessage) (result struct {
	TokensBefore         int64 `json:"tokensBefore"`
	EstimatedTokensAfter int64 `json:"estimatedTokensAfter"`
}) {
	_ = json.Unmarshal(raw, &result)
	return result
}

// piCompactTrigger maps Pi's reason for compacting onto the shared trigger.
// Pi compacts by itself at its threshold or after an overflow, which is auto;
// \"manual\" means someone asked for it.
func piCompactTrigger(reason string) string {
	if reason == "manual" {
		return "manual"
	}
	return "auto"
}

func decodePi(line []byte) Record {
	p, ok := parseRecord[piRecord](line)
	if !ok {
		return Record{}
	}
	r := Record{valid: true, recognized: true, recordUsage: recordUsage{Type: p.Type}}
	switch p.Type {
	case "session":
		r.SessionID = p.ID
		r.Events = []Event{{Kind: "status", Name: "session", State: "started"}}
	case "message_end":
		if p.Message.Role == "assistant" {
			r.Events = piEvents(p.Message.Content)
			r.recordUsage.Usage = p.Message.Usage
			r.ContextTokens = usageSum(p.Message.Usage, "input", "cacheRead", "cacheWrite")
		}
	case "message_update":
		// Final message_end is authoritative. Do not duplicate its text or usage.
	case "tool_execution_start", "tool_execution_update", "tool_execution_end":
		state, output := "started", ""
		if p.Type == "tool_execution_update" {
			// Partial results carry the same content envelope as final ones
			// (shipped docs/json.md), so streaming updates show their text too.
			state, output = "updated", piResultText(p.PartialResult)
		}
		if p.Type == "tool_execution_end" {
			state, output = "completed", piResultText(p.Result)
			if p.IsError {
				state = "failed"
			}
		}
		r.Events = []Event{{Kind: "tool", ID: p.ToolCallID, Name: p.ToolName, State: state, Input: compactJSON(p.Args), Output: output}}
		if p.ToolCallID != "" && p.Type != "tool_execution_update" {
			r.Signals = []ToolSignal{{ID: p.ToolCallID, Start: p.Type == "tool_execution_start"}}
		}
	case "compaction_start":
		r.Events = []Event{{Kind: "status", Name: "compaction", State: "started", Trigger: piCompactTrigger(p.Reason)}}
	case "compaction_end":
		// Pi omits result when compaction was aborted or failed.
		r.Compaction = !p.Aborted && len(p.Result) > 0 && string(p.Result) != "null"
		result := piCompactionResult(p.Result)
		r.Events = []Event{{Kind: "status", Name: "compaction", State: "completed",
			Trigger: piCompactTrigger(p.Reason), TokensBefore: result.TokensBefore, TokensAfter: result.EstimatedTokensAfter}}
		if !r.Compaction {
			r.Events[0].State, r.Events[0].Text = "failed", p.ErrorMessage
		}
	case "agent_start", "agent_end", "agent_settled", "turn_start", "turn_end", "message_start", "auto_retry_start", "auto_retry_end":
		r.Events = []Event{{Kind: "status", Name: p.Type}}
	default:
		return Record{valid: true}
	}
	return r
}

func PiOutput(stdout string) string {
	var output string
	for _, line := range strings.Split(stdout, "\n") {
		p, ok := parseRecord[piRecord]([]byte(line))
		if ok && p.Type == "message_end" && p.Message.Role == "assistant" {
			// An empty later assistant response must not expose an earlier answer.
			output = piText(p.Message.Content)
		}
	}
	return strings.TrimSpace(output)
}

// PiCleanStop reports whether the run ended on an assistant message with stop
// reason "stop" and did not abort. Such a run finished on purpose, even when
// its final message has no text.
func PiCleanStop(stdout string) bool {
	clean := false
	for _, line := range strings.Split(stdout, "\n") {
		p, ok := parseRecord[piRecord]([]byte(line))
		if !ok {
			continue
		}
		if p.Type == "message_end" && p.Message.Role == "assistant" {
			clean = p.Message.StopReason == "stop"
		}
		if p.Type == "agent_settled" && p.Aborted {
			clean = false
		}
	}
	return clean
}

func PiResultError(stdout string) error {
	var failure error
	for _, line := range strings.Split(stdout, "\n") {
		p, ok := parseRecord[piRecord]([]byte(line))
		if !ok {
			continue
		}
		if p.Type == "message_end" && p.Message.Role == "assistant" {
			failure = nil
			switch p.Message.StopReason {
			case "error", "aborted", "length":
				failure = providerfailure.TagDetail(fmt.Errorf("Pi response %s: %s", p.Message.StopReason, p.Message.ErrorMessage), p.Message.ErrorMessage)
			}
		}
		if p.Type == "agent_settled" && p.Aborted {
			failure = fmt.Errorf("Pi run aborted")
		}
	}
	return failure
}

func (n *UsageNormalizer) observePi(record recordUsage) (Usage, bool) {
	if record.Type != "message_end" || record.Usage == nil {
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
	// Pi reports uncached input separately from cache reads and writes.
	input := read("input", &n.input)
	cached := read("cacheRead", &n.cached)
	write := read("cacheWrite", &n.input)
	var cost struct {
		Total *float64 `json:"total"`
	}
	u := Usage{InputTokens: input + cached + write, CachedTokens: cached, OutputTokens: read("output", &n.output)}
	if json.Unmarshal(record.Usage["cost"], &cost) == nil && cost.Total != nil && *cost.Total >= 0 {
		u.CostUSD = *cost.Total
		n.cost.observed = true
	} else {
		n.cost.missing = true
	}
	return u, true
}
