package workeroutput

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/providerfailure"
)

// DialectClaudeStreamJSON is `claude -p --input-format stream-json
// --output-format stream-json --verbose --replay-user-messages`: dyna keeps
// stdin open, so steering and journal reminders are written as user messages
// that Claude takes in at its next tool boundary, without interrupting the
// worker. The bundled Claude profiles use it.
const DialectClaudeStreamJSON = "claude-stream-json"

type claudeRecord struct {
	recordUsage
	IsReplay  bool     `json:"isReplay"`
	Errors    []string `json:"errors"`
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	IsError   bool     `json:"is_error"`
	// APIErrorStatus and APIErrorCode are the provider's own failure fields on a
	// result. A refusal result also sets is_error, but keeps assistant text in
	// Result, so only these fields and Errors may name a provider reason.
	APIErrorStatus json.RawMessage `json:"api_error_status"`
	APIErrorCode   string          `json:"api_error_code"`
	// Status is raw because other records may use the key for non-strings.
	Status        json.RawMessage `json:"status"`
	CompactResult string          `json:"compact_result"`
	CompactError  string          `json:"compact_error"`
	// CompactMetadata is on the compact_boundary record: the trigger and the
	// context size on each side of the compaction.
	CompactMetadata struct {
		Trigger    string `json:"trigger"`
		PreTokens  int64  `json:"pre_tokens"`
		PostTokens int64  `json:"post_tokens"`
	} `json:"compact_metadata"`
	RateLimitInfo struct {
		// UnifiedWindows carries the five_hour and seven_day utilization as
		// fractions (0.18), with resets as Unix seconds.
		UnifiedWindows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
	// ParentToolUseID is set on messages of a subagent's own conversation,
	// whose prompt size says nothing about this session's context.
	ParentToolUseID string `json:"parent_tool_use_id"`
	Message         struct {
		Role string `json:"role"`
		// Usage is the one model call's token counts on an assistant message.
		Usage   map[string]json.RawMessage `json:"usage"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
}

func adaptClaudeStreamJSON(record *claudeRecord) ([]Event, bool) {
	switch record.Type {
	case "system":
		switch {
		case record.Subtype == "init":
			return []Event{{Kind: "status", Name: "session", State: "started"}}, true
		case record.Subtype == "compact_boundary":
			meta := record.CompactMetadata
			return []Event{{Kind: "status", Name: "compaction", State: "completed",
				Trigger: meta.Trigger, TokensBefore: meta.PreTokens, TokensAfter: meta.PostTokens}}, true
		case record.Subtype == "status" && string(record.Status) == `"compacting"`:
			return []Event{{Kind: "status", Name: "compaction", State: "started"}}, true
		case record.Subtype == "status" && record.CompactResult == "failed":
			return []Event{{Kind: "status", Name: "compaction", State: "failed", Text: record.CompactError}}, true
		}
		// Plumbing (commands_changed, thinking_tokens, task_* notices) is
		// status, not unparsed output.
		if record.Subtype != "" {
			return []Event{{Kind: "status", Name: record.Subtype}}, true
		}
	case "rate_limit_event":
		return []Event{{Kind: "status", Name: "rate_limit"}}, true
	case "assistant", "user":
		var events []Event
		for _, block := range record.Message.Content {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) != "" {
					role := record.Message.Role
					if role == "" {
						role = record.Type
					}
					events = append(events, Event{Kind: "message", Role: role, Text: block.Text})
				}
			case "thinking":
				if strings.TrimSpace(block.Thinking) != "" {
					events = append(events, Event{Kind: "reasoning", State: "completed", Text: block.Thinking})
				}
			case "tool_use":
				events = append(events, Event{Kind: "tool", Name: block.Name, State: "started", ID: block.ID, Input: compactJSON(block.Input)})
			case "tool_result":
				state := "completed"
				if block.IsError {
					state = "failed"
				}
				events = append(events, Event{Kind: "tool", Name: "tool_result", State: state, ID: block.ToolUseID, Output: claudeToolResultText(block.Content)})
			}
		}
		if len(events) > 0 {
			return events, true
		}
	case "result":
		state := record.Subtype
		if state == "" {
			state = "completed"
		}
		return []Event{{Kind: "status", Name: "result", State: state}}, true
	}
	return nil, false
}

func claudeToolResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return compactJSON(raw)
}

// ClaudeRateLimitWindow is one usage window of a Claude rate_limit_event,
// in the units saved readings keep: percent used and a reset time.
type ClaudeRateLimitWindow struct {
	ID          string
	UsedPercent float64
	ResetsAt    time.Time
}

// ClaudeRateLimitWindows reads the newest five-hour and weekly windows from
// a claude-stream-json stdout capture. The newest rate_limit_event with
// windows wins; older shapes without unifiedWindows report nothing.
func ClaudeRateLimitWindows(stdout string) []ClaudeRateLimitWindow {
	var windows []ClaudeRateLimitWindow
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[claudeRecord]([]byte(line))
		if !ok || record.Type != "rate_limit_event" {
			continue
		}
		var found []ClaudeRateLimitWindow
		for _, id := range []string{"five_hour", "seven_day"} {
			bucket, ok := record.RateLimitInfo.UnifiedWindows[id]
			if !ok || bucket.ResetsAt <= 0 {
				continue
			}
			percent := bucket.Utilization * 100
			if percent < 0 {
				percent = 0
			} else if percent > 100 {
				percent = 100
			}
			found = append(found, ClaudeRateLimitWindow{ID: id, UsedPercent: percent, ResetsAt: time.Unix(bucket.ResetsAt, 0).UTC()})
		}
		if len(found) > 0 {
			windows = found
		}
	}
	return windows
}

// ClaudeStreamResult extracts the final response and session id from a
// claude-stream-json stdout capture: the last `result` record (even empty) wins; otherwise
// the last assistant text message is used.
func ClaudeStreamResult(stdout string) (result, sessionID string) {
	var lastAssistant string
	hasResult := false
	for _, line := range strings.Split(stdout, "\n") {
		record, ok := parseRecord[claudeRecord]([]byte(line))
		if !ok {
			continue
		}
		if record.SessionID != "" && sessionID == "" {
			sessionID = record.SessionID
		}
		switch record.Type {
		case "result":
			hasResult = true
			result = strings.TrimSpace(record.Result)
		case "assistant":
			var text []string
			for _, block := range record.Message.Content {
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					text = append(text, strings.TrimSpace(block.Text))
				}
			}
			if len(text) > 0 {
				lastAssistant = strings.Join(text, "\n")
			}
		}
	}
	if !hasResult {
		result = lastAssistant
	}
	return result, sessionID
}

// ClaudeUserMessage encodes one user turn for `claude -p --input-format
// stream-json` (one JSON object per stdin line).
func ClaudeUserMessage(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}},
	})
	return append(b, '\n')
}

// ClaudeInputSignal classifies one stdout record of a persistent-input Claude
// process started with --replay-user-messages: replay is true when Claude
// emits a replay candidate. Callers must correlate its content with written
// input: internal task notifications can also be replayed users. Result is true
// at the end of each generation, not necessarily at process exit.
func ClaudeInputSignal(line string) (replay, result bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return false, false
	}
	var record struct {
		Type     string `json:"type"`
		IsReplay bool   `json:"isReplay"`
	}
	if json.Unmarshal([]byte(line), &record) != nil {
		return false, false
	}
	return record.Type == "user" && record.IsReplay, record.Type == "result"
}

// ClaudeReplayTexts returns the text of each message a replay acknowledges.
// String content is one message. Claude merges messages it takes in at the
// same tool boundary into one replay with one text block per message.
// Harness-internal origins, unknown content, and other payloads are not acks.
func ClaudeReplayTexts(line string) ([]string, bool) {
	var r struct {
		Type     string          `json:"type"`
		IsReplay bool            `json:"isReplay"`
		Origin   json.RawMessage `json:"origin"`
		Message  struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &r) != nil || r.Type != "user" || !r.IsReplay ||
		(len(r.Origin) > 0 && string(r.Origin) != "null") || (r.Message.Role != "" && r.Message.Role != "user") {
		return nil, false
	}
	var content *string
	if json.Unmarshal(r.Message.Content, &content) == nil && content != nil {
		return []string{*content}, true
	}
	var blocks []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(r.Message.Content, &blocks) != nil || len(blocks) == 0 {
		return nil, false
	}
	texts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" || b.Text == nil {
			return nil, false
		}
		texts = append(texts, *b.Text)
	}
	return texts, true
}

// ClaudeResultError preserves protocol failure even if retirement succeeds or a
// later harness-background generation emits success before the process exits.
// ClaudeCleanStop reports whether the stream ended with a successful result
// record. Such a run finished on purpose, even when its result text is empty.
func ClaudeCleanStop(stdout string) bool {
	clean := false
	for _, line := range strings.Split(stdout, "\n") {
		r, ok := parseRecord[claudeRecord]([]byte(line))
		if ok && r.Type == "result" {
			clean = !r.IsError && r.Subtype == "success"
		}
	}
	return clean
}

func ClaudeResultError(line string) error {
	r, ok := parseRecord[claudeRecord]([]byte(line))
	if !ok || r.Type != "result" || (!r.IsError && !strings.HasPrefix(r.Subtype, "error")) {
		return nil
	}

	detail := strings.TrimSpace(strings.Join(append([]string{r.Result}, r.Errors...), " "))
	err := fmt.Errorf("Claude protocol result %s: %s", r.Subtype, detail)
	// The provider's own status and code name the reason. A refusal result puts
	// assistant text in Result, so Result is display text only.
	if reason, ok := r.Reason(); ok {
		err = providerfailure.Tag(reason, err)
	}
	return err
}

// Reason returns the provider reason of a failed result. Only the provider's
// structured fields name it: its API error status, its API error code, or its
// error list. Assistant prose never does.
func (r claudeRecord) Reason() (providerfailure.Reason, bool) {
	if status, err := strconv.Atoi(strings.TrimSpace(scalarText(r.APIErrorStatus))); err == nil {
		if reason, ok := providerfailure.StatusReason(status); ok {
			return reason, true
		}
	}
	if reason, ok := providerfailure.ClassifyField(r.APIErrorCode); ok {
		return reason, true
	}
	for _, message := range r.Errors {
		if reason, ok := providerfailure.Classify(message); ok {
			return reason, true
		}
	}
	return "", false
}

// ClaudeGenerationStarted recognizes affirmative generation activity. This is
// distinct from an input acknowledgment and from a protocol terminal: the
// acknowledgment stall bound must never become a quiet-tool/model timeout.
func ClaudeGenerationStarted(line string) bool {
	var r struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if json.Unmarshal([]byte(line), &r) != nil {
		return false
	}
	return r.Type == "assistant" || r.Type == "stream_event" || (r.Type == "system" && r.Subtype == "init")
}
