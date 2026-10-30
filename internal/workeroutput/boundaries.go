package workeroutput

import (
	"encoding/json"
	"strings"
	"sync"
)

// ToolSignal is one tool-call boundary read from a raw stdout record. A start
// opens the call named by ID; an end closes it. IDs are only meaningful within
// one dialect stream.
type ToolSignal struct {
	ID    string
	Start bool
}

// SupportsToolBoundaries reports whether the declared dialect's stream marks
// where work starts and finishes. Dialects that do not (plain text and
// undeclared profiles) never report in-flight work, so callers
// keep their immediate-delivery behaviour for them.
func SupportsToolBoundaries(decl *Declaration) bool {
	if decl == nil {
		return false
	}
	return Capabilities(decl.Dialect).SafePoints
}

// ToolSignals extracts tool-call start/completion signals from one raw line.
func ToolSignals(decl *Declaration, line string) []ToolSignal {
	if !SupportsToolBoundaries(decl) || strings.TrimSpace(line) == "" {
		return nil
	}
	return DecodeLine(decl, line).Signals
}

func claudeToolSignals(record *claudeRecord) []ToolSignal {
	var out []ToolSignal
	for _, block := range record.Message.Content {
		switch {
		case record.Type == "assistant" && block.Type == "tool_use" && block.ID != "":
			out = append(out, ToolSignal{ID: block.ID, Start: true})
		case record.Type == "user" && block.Type == "tool_result" && block.ToolUseID != "":
			out = append(out, ToolSignal{ID: block.ToolUseID})
		}
	}
	return out
}

// ToolTracker follows which tool calls are in flight on a live stream. It is
// safe for one writer (the stdout observer) and concurrent readers.
type ToolTracker struct {
	decl     *Declaration
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func NewToolTracker(decl *Declaration) *ToolTracker {
	return &ToolTracker{decl: CloneDeclaration(decl), inFlight: map[string]struct{}{}}
}

// Supported reports whether the stream exposes tool boundaries at all.
func (t *ToolTracker) Supported() bool {
	return t != nil && SupportsToolBoundaries(t.decl)
}

// Observe consumes one raw line and reports whether a tool call completed.
func (t *ToolTracker) Observe(line string) (completed bool) {
	if !t.Supported() {
		return false
	}
	return t.ObserveSignals(ToolSignals(t.decl, line))
}

// ObserveSignals consumes boundaries from a record already decoded by the caller.
func (t *ToolTracker) ObserveSignals(signals []ToolSignal) (completed bool) {
	if !t.Supported() {
		return false
	}
	if len(signals) == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range signals {
		if s.Start {
			t.inFlight[s.ID] = struct{}{}
			continue
		}
		if _, ok := t.inFlight[s.ID]; ok {
			delete(t.inFlight, s.ID)
			completed = true
		}
	}
	return completed
}

// InFlight is the number of tool calls started and not yet completed.
func (t *ToolTracker) InFlight() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inFlight)
}

func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.CachedTokens += o.CachedTokens
	u.OutputTokens += o.OutputTokens
	u.CostUSD += o.CostUSD
}

// ExtractUsage reads token usage from one raw line when the dialect reports it
// (claude result). Other values are raw thread snapshots; use
// UsageNormalizer for additive per-call accounting.
func ExtractUsage(decl *Declaration, line string) (Usage, bool) {
	if decl == nil || strings.TrimSpace(line) == "" {
		return Usage{}, false
	}
	decoded := DecodeLine(decl, line)
	if !decoded.valid {
		return Usage{}, false
	}
	record := decoded.recordUsage
	var u struct{ Input, Output, Cached, CacheRead, CacheCreation int64 }
	validUsage := true
	read := func(key string) int64 {
		raw, ok := record.Usage[key]
		if !ok {
			return 0
		}
		var value int64
		if json.Unmarshal(raw, &value) != nil {
			validUsage = false
		}
		return value
	}
	u.Input, u.Output, u.Cached = read("input_tokens"), read("output_tokens"), read("cached_input_tokens")
	u.CacheRead, u.CacheCreation = read("cache_read_input_tokens"), read("cache_creation_input_tokens")
	if !validUsage {
		return Usage{}, false
	}
	cost := 0.0
	if record.Cost != nil {
		cost = *record.Cost
	}
	switch strings.TrimSpace(decl.Dialect) {
	case DialectClaudeStreamJSON:
		if record.Type != "result" {
			return Usage{}, false
		}
		in := u.Input + u.CacheRead + u.CacheCreation
		return Usage{InputTokens: in, CachedTokens: u.CacheRead, OutputTokens: u.Output, CostUSD: cost}, in+u.Output > 0
	}
	return Usage{}, false
}
