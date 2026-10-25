package workeroutput

import (
	"encoding/json"
	"strings"
)

// UsageNormalization describes observed coverage, not billing verification.
// Absent metadata identifies legacy records whose counters have not been normalized.
type UsageNormalization struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Input       string `json:"input"`
	CachedInput string `json:"cachedInput"`
	Output      string `json:"output"`
	Cost        string `json:"cost"`
	// CostBasis is CostBasisTurn when Cost holds this turn's own spend. Claude
	// turns saved before it was set hold the session's cumulative total.
	CostBasis string `json:"costBasis,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// CostBasisTurn marks a cost that is the turn's own step, not a session total.
const CostBasisTurn = "turn"

// Merge combines coverage for calls within a Dyna turn. An empty value is identity.
func (n *UsageNormalization) Merge(other UsageNormalization) {
	if other.Version == 0 {
		return
	}
	if n.Version == 0 {
		*n = other
		return
	}
	merge := func(a, b string) string {
		if a == b {
			return a
		}
		return "partial"
	}
	n.Input = merge(n.Input, other.Input)
	n.CachedInput = merge(n.CachedInput, other.CachedInput)
	n.Output = merge(n.Output, other.Output)
	n.Cost = merge(n.Cost, other.Cost)
	if n.Source != other.Source {
		n.Source = "mixed"
	}
	if n.CostBasis != other.CostBasis {
		n.CostBasis = ""
	}
	if other.Reason != "" && !strings.Contains(n.Reason, other.Reason) {
		if n.Reason != "" {
			n.Reason += "; "
		}
		n.Reason += other.Reason
	}
}

// MarkPartial records an unobserved tail after an invocation fails.
func (n *UsageNormalization) MarkPartial(reason string) {
	for _, field := range []*string{&n.Input, &n.CachedInput, &n.Output, &n.Cost} {
		if *field == "complete" {
			*field = "partial"
		}
	}
	if n.Reason != "" {
		n.Reason += "; "
	}
	n.Reason += reason
}

// UsageBaseline is a native thread's last cumulative snapshot, never activity.
// Pointers preserve the distinction between a reported zero and a missing field.
type UsageBaseline struct {
	Version  int    `json:"version"`
	ThreadID string `json:"threadId"`
	// Cost is Claude Code's session-cumulative total_cost_usd after the turn.
	Cost *float64 `json:"cost,omitempty"`
}

type metricCoverage struct{ observed, missing bool }

func (m metricCoverage) state() string {
	if !m.observed {
		return "unknown"
	}
	if m.missing {
		return "partial"
	}
	return "complete"
}

// UsageNormalizer converts native usage reports to additive call deltas. It
// survives process restarts within a logical call. Callers persist Baseline
// between calls for Claude's cumulative cost.
type UsageNormalizer struct {
	baseline                    *UsageBaseline
	threadID                    string
	fresh                       bool
	input, cached, output, cost metricCoverage
	reason                      string
	costBase                    *float64 // Claude's cumulative total_cost_usd already billed
	costSeen                    bool
}

func NewUsageNormalizer(resumeID string, baseline *UsageBaseline) *UsageNormalizer {
	n := &UsageNormalizer{threadID: resumeID, fresh: resumeID == ""}
	if baseline != nil && baseline.Version == 1 && baseline.ThreadID == resumeID && resumeID != "" {
		b := *baseline
		valid := b.Cost == nil || *b.Cost >= 0
		if valid {
			n.baseline = &b
			n.costBase = b.Cost
		}
	}
	if n.fresh {
		zero := 0.0
		n.costBase = &zero
	}
	return n
}
func (n *UsageNormalizer) markReason(reason string) {
	if !strings.Contains(n.reason, reason) {
		if n.reason != "" {
			n.reason += "; "
		}
		n.reason += reason
	}
}
func (n *UsageNormalizer) Baseline() *UsageBaseline {
	if !n.costSeen || n.threadID == "" || n.costBase == nil {
		return nil
	}
	cost := *n.costBase
	return &UsageBaseline{Version: 1, ThreadID: n.threadID, Cost: &cost}
}
func (n *UsageNormalizer) Normalization() UsageNormalization {
	return UsageNormalization{Version: 1, Source: "harness-reported", Input: n.input.state(), CachedInput: n.cached.state(), Output: n.output.state(), Cost: n.cost.state(), CostBasis: CostBasisTurn, Reason: n.reason}
}

// Observe returns only newly observed usage. Unsupported or missing metrics
// remain unknown. Counter discontinuities establish a baseline without billing
// the whole snapshot to the current turn.
func (n *UsageNormalizer) Observe(decl *Declaration, line string) (Usage, bool) {
	return n.ObserveRecord(DecodeLine(decl, line))
}

// ObserveRecord updates accounting from an already decoded envelope.
func (n *UsageNormalizer) ObserveRecord(decoded Record) (Usage, bool) {
	if !decoded.valid {
		return Usage{}, false
	}
	dialect := decoded.dialect
	record := decoded.recordUsage
	if dialect == DialectPiJSON {
		return n.observePi(record)
	}
	if dialect == DialectMuseJSONL && (record.Type == "run.terminal.completed" || record.Type == "run.terminal.failed") {
		// muse exec --json omits provider usage, even though Muse stores it
		// internally. Do not turn an absent report into measured zero usage.
		n.markReason(Capabilities(dialect).UsageReason)
		return Usage{}, false
	}
	if dialect == DialectText {
		n.markReason(Capabilities(dialect).UsageReason)
		return Usage{}, false
	}
	if (dialect == DialectOpenCodeJSON && record.Type != "step_finish") ||
		(dialect == DialectClaudeStreamJSON && record.Type != "result") {
		return Usage{}, false
	}
	if dialect != DialectClaudeStreamJSON && dialect != DialectOpenCodeJSON {
		return Usage{}, false
	}
	if record.Usage == nil {
		return Usage{}, false
	}
	read := func(key string) *int64 {
		raw, ok := record.Usage[key]
		if !ok || string(raw) == "null" {
			return nil
		}
		var v int64
		if json.Unmarshal(raw, &v) != nil || v < 0 {
			return nil
		}
		return &v
	}
	in, cached, out := read("input_tokens"), read("cached_input_tokens"), read("output_tokens")
	if dialect == DialectClaudeStreamJSON || dialect == DialectOpenCodeJSON {
		readCache, writeCache := read("cache_read_input_tokens"), read("cache_creation_input_tokens")
		cached = readCache
		if in != nil {
			v := *in
			if readCache != nil {
				v += *readCache
			} else {
				n.input.missing = true
			}
			if writeCache != nil {
				v += *writeCache
			} else {
				n.input.missing = true
			}
			in = &v
		}
		if dialect == DialectOpenCodeJSON && out != nil {
			// OpenCode separates visible output from reasoning tokens, and
			// uncached input from cache reads and writes. Dyna totals include both.
			if reasoning := read("reasoning_tokens"); reasoning != nil {
				v := *out + *reasoning
				out = &v
			} else {
				n.output.missing = true
			}
		}
	}
	value := func(v *int64, c *metricCoverage) int64 {
		if v == nil {
			c.missing = true
			return 0
		}
		c.observed = true
		return *v
	}
	u := Usage{InputTokens: value(in, &n.input), CachedTokens: value(cached, &n.cached), OutputTokens: value(out, &n.output)}
	if dialect == DialectOpenCodeJSON && record.Cost != nil && *record.Cost >= 0 {
		// Each step reports an additive cost, not a process snapshot.
		u.CostUSD = *record.Cost
		n.cost.observed = true
	} else if dialect == DialectClaudeStreamJSON && record.Cost != nil && *record.Cost >= 0 {
		// total_cost_usd is cumulative for the Claude session across resumed
		// turns, so a turn's cost is the step from the saved total.
		total := *record.Cost
		switch {
		case record.ThreadID != "" && n.threadID != "" && n.threadID != record.ThreadID:
			n.cost.missing = true
			n.markReason("claude session changed")
		case n.costBase == nil:
			n.cost.missing = true
			n.markReason("cost baseline unavailable")
		case total >= *n.costBase:
			u.CostUSD = total - *n.costBase
			n.cost.observed = true
		default:
			n.cost.missing = true
			n.markReason("cost counter reset")
		}
		if record.ThreadID != "" {
			n.threadID = record.ThreadID
		}
		n.costBase, n.costSeen = &total, true
	} else {
		n.cost.missing = true
	}
	return u, true
}
