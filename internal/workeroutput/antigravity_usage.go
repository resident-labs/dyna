package workeroutput

import "encoding/json"

// Antigravity reports per-step usage, then repeats the process total in result.
// Keep completed steps visible during a run and add only the terminal remainder.
func (n *UsageNormalizer) observeAntigravity(record recordUsage) (Usage, bool) {
	if record.Usage == nil || (record.Type != "result" && record.StepID == "") {
		return Usage{}, false
	}
	valid := map[string]bool{}
	read := func(key string, coverage *metricCoverage) int64 {
		raw, exists := record.Usage[key]
		var v int64
		if !exists || string(raw) == "null" || json.Unmarshal(raw, &v) != nil || v < 0 {
			if coverage != nil {
				coverage.missing = true
			}
			return 0
		}
		valid[key] = true
		if coverage != nil {
			coverage.observed = true
		}
		return v
	}
	u := Usage{InputTokens: read("input_tokens", &n.input), OutputTokens: read("output_tokens", &n.output), CachedTokens: read("cache_read_tokens", &n.cached)}
	// The native total resolves whether thinking tokens are separate from output
	// or already included. Never add thinking twice when total includes it.
	total := read("total_tokens", nil)
	if valid["input_tokens"] && valid["output_tokens"] && total >= u.Total() && total > 0 {
		u.OutputTokens = total - u.InputTokens
	}
	// Antigravity reports cache reads beside input_tokens, not inside it. Dyna's
	// input includes cached input, so add the reads back.
	u.InputTokens += u.CachedTokens
	if !valid["cache_read_tokens"] {
		n.input.missing = true // the input total lacks its cache reads
	}
	n.cost.missing = true // No USD cost is present in this protocol's usage object.
	previous := n.antigravityTotal
	if record.Type == "step_update" {
		if n.antigravitySteps == nil {
			n.antigravitySteps = map[string]Usage{}
		}
		previous = n.antigravitySteps[record.StepID]
	}
	delta := Usage{InputTokens: u.InputTokens - previous.InputTokens, OutputTokens: u.OutputTokens - previous.OutputTokens, CachedTokens: u.CachedTokens - previous.CachedTokens}
	if delta.InputTokens < 0 || delta.OutputTokens < 0 || delta.CachedTokens < 0 {
		n.markReason("Antigravity usage counter decreased")
		n.input.missing = true
		n.output.missing = true
		n.cached.missing = true
	}
	delta.InputTokens = max(0, delta.InputTokens)
	delta.OutputTokens = max(0, delta.OutputTokens)
	delta.CachedTokens = max(0, delta.CachedTokens)
	if record.Type == "step_update" {
		previous.Add(delta)
		n.antigravitySteps[record.StepID] = previous
	}
	n.antigravityTotal.Add(delta)
	return delta, true
}
