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
	Version     int    `json:"version"`
	ThreadID    string `json:"threadId"`
	Input       *int64 `json:"input,omitempty"`
	CachedInput *int64 `json:"cachedInput,omitempty"`
	Output      *int64 `json:"output,omitempty"`
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
// between calls for Codex cumulative thread snapshots.
type UsageNormalizer struct {
	baseline                    *UsageBaseline
	threadID                    string
	fresh                       bool
	input, cached, output, cost metricCoverage
	reason                      string
	costBase                    *float64 // Claude's cumulative total_cost_usd already billed
	costSeen                    bool
	lastSnapshot                bool
}

func NewUsageNormalizer(resumeID string, baseline *UsageBaseline) *UsageNormalizer {
	n := &UsageNormalizer{threadID: resumeID, fresh: resumeID == ""}
	if baseline != nil && baseline.Version == 1 && baseline.ThreadID == resumeID && resumeID != "" {
		b := *baseline
		valid := true
		for _, v := range []*int64{b.Input, b.CachedInput, b.Output} {
			if v != nil && *v < 0 {
				valid = false
			}
		}
		if b.Input != nil && b.CachedInput != nil && *b.CachedInput > *b.Input {
			valid = false
		}
		if b.Cost != nil && *b.Cost < 0 {
			valid = false
		}
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
func (n *UsageNormalizer) BeginProcess() {
	n.lastSnapshot = false
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
	if n.costSeen && n.threadID != "" && n.costBase != nil {
		cost := *n.costBase
		return &UsageBaseline{Version: 1, ThreadID: n.threadID, Cost: &cost}
	}
	if n.baseline == nil || !n.lastSnapshot {
		return nil
	}
	b := *n.baseline
	return &b
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
	if dialect == DialectCodexJSONL && record.Type == "thread.started" && record.ThreadID != "" {
		if n.threadID != "" && n.threadID != record.ThreadID {
			n.baseline = nil
			n.fresh = false
			n.input.missing = true
			n.cached.missing = true
			n.output.missing = true
			n.markReason("native thread changed")
		}
		n.threadID = record.ThreadID
		return Usage{}, false
	}
	if (dialect == DialectOpenCodeJSON && record.Type != "step_finish") ||
		(dialect == DialectCodexJSONL && record.Type != "turn.completed") ||
		(dialect == DialectClaudeStreamJSON && record.Type != "result") {
		return Usage{}, false
	}
	if dialect != DialectCodexJSONL && dialect != DialectClaudeStreamJSON && dialect != DialectOpenCodeJSON {
		return Usage{}, false
	}
	if record.Usage == nil {
		if dialect == DialectCodexJSONL {
			n.baseline = nil
			n.fresh = false
			n.lastSnapshot = false
			n.input.missing = true
			n.cached.missing = true
			n.output.missing = true
			n.markReason("terminal usage unavailable")
		}
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
	if dialect == DialectCodexJSONL {
		if in != nil && cached != nil && *cached > *in {
			cached = nil
			n.cached.missing = true
			n.markReason("cached input exceeds total input")
		}
		n.lastSnapshot = true
		current := &UsageBaseline{Version: 1, ThreadID: n.threadID, Input: in, CachedInput: cached, Output: out}
		prior := n.baseline
		if prior == nil && n.fresh && n.threadID != "" {
			zero := int64(0)
			prior = &UsageBaseline{Input: &zero, CachedInput: &zero, Output: &zero}
		}
		reset := false
		if prior != nil {
			for _, pair := range [][2]*int64{{in, prior.Input}, {cached, prior.CachedInput}, {out, prior.Output}} {
				if pair[0] != nil && pair[1] != nil && *pair[0] < *pair[1] {
					reset = true
				}
			}
		}
		if reset {
			prior = nil
			n.markReason("cumulative counter reset")
		}
		if prior == nil {
			n.markReason("cumulative baseline unavailable")
		}
		delta := func(now, old *int64, c *metricCoverage) int64 {
			if now == nil || old == nil {
				c.missing = true
				return 0
			}
			c.observed = true
			return *now - *old
		}
		if prior == nil {
			prior = &UsageBaseline{}
		}
		u := Usage{InputTokens: delta(in, prior.Input, &n.input), CachedTokens: delta(cached, prior.CachedInput, &n.cached), OutputTokens: delta(out, prior.Output, &n.output)}
		if n.threadID != "" {
			n.baseline = current
		}
		n.fresh = false
		return u, true
	}
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
	} else {
		if in == nil {
			in = read("inputTokens")
		}
		if out == nil {
			out = read("outputTokens")
		}
		cached = nil
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
