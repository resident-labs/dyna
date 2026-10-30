package workeroutput

import (
	"encoding/json"
	"strings"
)

// Record separates execution signals from filtered presentation events.
// DecodeLine parses each vendor envelope once for all live observers.
type Record struct {
	Events     []Event
	Signals    []ToolSignal
	SessionID  string
	Compaction bool
	// Generation reports model work (thinking, tool use, or produced text)
	// that the record shows even when display filtering drops every event, as
	// with a reasoning item that starts before it has any text.
	Generation bool
	// ContextTokens is the prompt size of one model call, cached tokens
	// included, when the record reports a single call. Dyna's compact
	// trigger compares the last one with the profile's threshold.
	ContextTokens int64
	// Call is the usage of one model call, when the record reports one. Nil
	// means the dialect reports no per-call usage on this record.
	Call              *CallUsage
	dialect           string
	valid, recognized bool
	recordUsage
}

// Recognized reports whether the adapter understood this vendor record.
// Plumbing records can be recognized without creating presentation events.
func (r Record) Recognized() bool { return r.recognized }

// Generates reports whether the record shows the model working: thinking, tool
// use, or produced text. It is independent of display filtering, so a reasoning
// item that starts before it has any text still counts as generation.
func (r Record) Generates() bool {
	if r.Generation || len(r.Signals) > 0 || len(r.Usage) > 0 || r.Cost != nil {
		return true
	}
	for _, e := range r.Events {
		switch e.Kind {
		case "reasoning", "tool", "usage":
			return true
		case "message":
			if strings.TrimSpace(e.Text) != "" {
				return true
			}
		}
	}
	return false
}

type recordUsage struct {
	Type     string                     `json:"-"`
	StepID   string                     `json:"-"`
	ThreadID string                     `json:"-"`
	Usage    map[string]json.RawMessage `json:"usage"`
	Cost     *float64                   `json:"total_cost_usd"`
}

// callUsage reads one model call's prompt counts from its usage object.
func callUsage(usage map[string]json.RawMessage) *CallUsage {
	if usage == nil {
		return nil
	}
	in, read, write := usageField(usage, "input"), usageField(usage, "cacheRead"), usageField(usage, "cacheWrite")
	c := &CallUsage{Input: in, CacheRead: read, CacheWrite: write}
	if in != nil && read != nil && write != nil {
		total := *in + *read + *write
		c.Context = &total
	}
	return c
}

// usageField reads one non-negative integer usage field. A missing or invalid
// field is nil, so the caller can tell it apart from a reported zero.
func usageField(usage map[string]json.RawMessage, key string) *int64 {
	raw, ok := usage[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	var v int64
	if json.Unmarshal(raw, &v) != nil || v < 0 {
		return nil
	}
	return &v
}

// usageSum adds the named non-negative integer usage fields.
func usageSum(usage map[string]json.RawMessage, keys ...string) int64 {
	var sum int64
	for _, k := range keys {
		var v int64
		if json.Unmarshal(usage[k], &v) == nil && v > 0 {
			sum += v
		}
	}
	return sum
}

func parseRecord[T any](line []byte) (*T, bool) {
	var record T
	if json.Unmarshal(line, &record) != nil {
		return nil, false
	}
	return &record, true
}

// DecodeLine preserves raw records and tool boundaries independently of display filtering.
func DecodeLine(decl *Declaration, line string) Record {
	var r Record
	if decl != nil {
		r.dialect = strings.TrimSpace(decl.Dialect)
		if decode := adapters[r.dialect]; decode != nil {
			r = decode([]byte(line))
			r.dialect = strings.TrimSpace(decl.Dialect)
		}
	}
	if !r.recognized {
		r.Events = []Event{{V: SchemaVersion, Kind: "raw", Raw: line}}
		return r
	}
	normalized := r.Events[:0]
	for _, e := range r.Events {
		if e.Kind == "tool" && (e.State == "completed" || e.State == "failed") && e.ID == "" {
			continue
		}
		e.V = SchemaVersion
		normalized = append(normalized, e)
	}
	r.Events = normalized
	return r
}

func decodeText(line []byte) Record {
	events, ok := adaptText(line)
	return Record{Events: events, recognized: ok, valid: true}
}

func decodeClaude(line []byte) Record {
	p, ok := parseRecord[claudeRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptClaudeStreamJSON(p)
	p.recordUsage.Type, p.recordUsage.ThreadID = p.Type, p.SessionID
	var context int64
	if p.Type == "assistant" && p.ParentToolUseID == "" {
		context = usageSum(p.Message.Usage, "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
	}
	return Record{Events: events, Signals: claudeToolSignals(p), SessionID: p.SessionID, Compaction: p.Type == "system" && p.Subtype == "compact_boundary", ContextTokens: context, valid: true, recognized: known, recordUsage: p.recordUsage}
}

// scalarText reads a structured field that is a JSON string or number. A field
// that is an object or array names nothing by itself.
func scalarText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}
