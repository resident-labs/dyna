package workeroutput

import (
	"encoding/json"
	"strings"

	"dyna-agent/internal/providerfailure"
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
	ContextTokens     int64
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
		if e.Kind == "tool" && (e.State == "completed" || e.State == "failed") && e.ID == "" && r.dialect != DialectAntigravityStreamJSON {
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

func decodeCodex(line []byte) Record {
	p, ok := parseRecord[codexRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptCodexJSONL(p)
	p.recordUsage.Type, p.recordUsage.ThreadID = p.Type, p.ThreadID
	r := Record{Events: events, Signals: codexToolSignals(p), valid: true, recognized: known, recordUsage: p.recordUsage}
	if p.Type == "thread.started" {
		r.SessionID = p.ThreadID
	}
	// A completed compaction item is the only Codex success signal.
	if p.Type == "item.completed" && (p.Item.Type == "context_compaction" || p.Item.Type == "contextCompaction") {
		r.Compaction = true
	}
	// An accepted turn or item start is model work before it has display text.
	// Session creation alone does not show that the model started working.
	if p.Type == "turn.started" || (strings.HasPrefix(p.Type, "item.") && codexItemGenerates(p.Item.Type)) {
		r.Generation = true
	}
	return r
}

func decodeAntigravity(line []byte) Record {
	p, ok := parseRecord[antigravityRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptAntigravityStreamJSON(p)
	usage := recordUsage{Type: p.Event}
	if p.Event == "result" {
		usage.Usage = p.Result.Usage
	} else if p.Event == "step_update" && strings.EqualFold(p.StepUpdate.State, "DONE") && p.StepUpdate.Index != nil {
		usage.Usage = p.StepUpdate.Usage
		usage.StepID = itoa(*p.StepUpdate.Index)
	}
	r := Record{Events: events, Signals: antigravityToolSignals(p), valid: true, recognized: known, recordUsage: usage}
	if p.Event == "step_update" {
		// Step usage is per call; result usage is the process total.
		r.ContextTokens = usageSum(usage.Usage, "input_tokens", "cache_read_tokens")
	}
	if p.Event == "init" {
		r.SessionID = p.ConversationID
	}
	if p.Event == "result" {
		r.SessionID = p.Result.ConversationID
	}
	return r
}

func decodeOpenCode(line []byte) Record {
	p, ok := parseRecord[openCodeRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptOpenCodeJSON(p)
	usage := recordUsage{Type: p.Type}
	if p.Type == "step_finish" {
		usage.Cost = p.Part.Cost
		if p.Part.Tokens != nil {
			usage.Usage = map[string]json.RawMessage{}
			for native, normalized := range map[string]string{"input": "input_tokens", "output": "output_tokens", "reasoning": "reasoning_tokens"} {
				if raw, ok := p.Part.Tokens[native]; ok {
					usage.Usage[normalized] = raw
				}
			}
			var cache map[string]json.RawMessage
			if json.Unmarshal(p.Part.Tokens["cache"], &cache) == nil {
				usage.Usage["cache_read_input_tokens"] = cache["read"]
				usage.Usage["cache_creation_input_tokens"] = cache["write"]
			}
		}
	}
	return Record{Events: events, Signals: openCodeToolSignals(p), SessionID: p.SessionID, valid: true, recognized: known, recordUsage: usage}
}

func decodeClaude(line []byte) Record {
	p, ok := parseRecord[claudeRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptClaudeStreamJSON(p)
	p.recordUsage.Type, p.recordUsage.ThreadID = p.Type, p.SessionID
	return Record{Events: events, Signals: claudeToolSignals(p), SessionID: p.SessionID, Compaction: p.Type == "system" && p.Subtype == "compact_boundary", valid: true, recognized: known, recordUsage: p.recordUsage}
}

func decodeMuse(line []byte) Record {
	p, ok := parseRecord[museRecord](line)
	if !ok {
		return Record{}
	}
	events, known := adaptMuseJSONL(p)
	r := Record{Events: events, Signals: museToolSignals(p), valid: true, recognized: known, recordUsage: recordUsage{Type: p.PayloadType, Usage: p.Payload.Usage}}
	if p.Stream.Kind == "session" {
		r.SessionID = p.Stream.ID
	}
	r.ContextTokens = p.Payload.ContextTokens
	r.Generation = p.PayloadType == "run.reasoning.delta" || p.PayloadType == "run.lifecycle.started" || p.PayloadType == "run.usage"
	return r
}

type codexRecord struct {
	recordUsage
	Type     string      `json:"type"`
	ThreadID string      `json:"thread_id"`
	Message  string      `json:"message"`
	Error    recordError `json:"error"`
	Item     struct {
		ID               string          `json:"id"`
		ExitCode         *int            `json:"exit_code"`
		Status           string          `json:"status"`
		Type             string          `json:"type"`
		Text             string          `json:"text"`
		Command          string          `json:"command"`
		AggregatedOutput string          `json:"aggregated_output"`
		Server           string          `json:"server"`
		Tool             string          `json:"tool"`
		Query            string          `json:"query"`
		Message          string          `json:"message"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           json.RawMessage `json:"result"`
		Changes          []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
}

type openCodeRecord struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionID"`
	Error     json.RawMessage `json:"error"`
	Part      struct {
		Text      string                     `json:"text"`
		MessageID string                     `json:"messageID"`
		ID        string                     `json:"id"`
		CallID    string                     `json:"callID"`
		Tool      string                     `json:"tool"`
		Reason    string                     `json:"reason"`
		Cost      *float64                   `json:"cost"`
		Tokens    map[string]json.RawMessage `json:"tokens"`
		State     struct {
			Status string          `json:"status"`
			Input  json.RawMessage `json:"input"`
			Output json.RawMessage `json:"output"`
			Error  string          `json:"error"`
		} `json:"state"`
	} `json:"part"`
}

type antigravityRecord struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	StepUpdate     struct {
		State     string                     `json:"state"`
		Usage     map[string]json.RawMessage `json:"usage"`
		Index     *int                       `json:"step_index"`
		StepType  string                     `json:"step_type"`
		ToolName  string                     `json:"tool_name"`
		TextDelta string                     `json:"text_delta"`
		ToolInfo  struct {
			Name       string            `json:"name"`
			Parameters json.RawMessage   `json:"parameters"`
			Output     json.RawMessage   `json:"output"`
			Error      *antigravityError `json:"error"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Command struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	} `json:"command"`
	Result struct {
		Usage          map[string]json.RawMessage `json:"usage"`
		Status         string                     `json:"status"`
		ConversationID string                     `json:"conversation_id"`
		Response       string                     `json:"response"`
		Error          string                     `json:"error"`
		Command        struct {
			Name string          `json:"name"`
			Data json.RawMessage `json:"data"`
		} `json:"command"`
	} `json:"result"`
}

// recordError accepts both protocol error shapes without rejecting the envelope.
// It keeps the provider's structured type, code and status, because they name a
// failure on their own; the message is only a fallback.
type recordError struct {
	Message string
	Type    string
	Code    string
	Status  string
}

func (e *recordError) UnmarshalJSON(raw []byte) error {
	var object struct {
		Message string          `json:"message"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
		Status  json.RawMessage `json:"status"`
	}
	if json.Unmarshal(raw, &object) == nil {
		e.Message = object.Message
		e.Type, e.Code, e.Status = scalarText(object.Type), scalarText(object.Code), scalarText(object.Status)
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		e.Message = text
	}
	return nil
}

// Reason returns the provider reason of this error. A structured type, code or
// status is preferred, because the message alone may use no provider words. The
// fields are a provider diagnostic, not model output, so this is a trusted
// boundary.
func (e recordError) Reason() (providerfailure.Reason, bool) {
	for _, field := range []string{e.Type, e.Code, e.Status} {
		if reason, ok := providerfailure.ClassifyField(field); ok {
			return reason, true
		}
	}
	return providerfailure.Classify(e.Message)
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
