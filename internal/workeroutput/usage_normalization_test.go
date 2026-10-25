package workeroutput

import (
	"fmt"
	"testing"
)

func TestClaudeCacheReadsAreSubsetAndCostIsCumulativeAcrossProcesses(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	d := &Declaration{Dialect: DialectClaudeStreamJSON}
	line := `{"type":"result","usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":10,"output_tokens":5},"total_cost_usd":0.5}`
	first, _ := n.Observe(d, line)
	repeated, _ := n.Observe(d, line)
	next, _ := n.Observe(d, line)
	if first.InputTokens != 100 || first.CachedTokens != 80 || first.CostUSD != 0.5 || repeated.CostUSD != 0 || next.CostUSD != 0 {
		t.Fatalf("%#v %#v %#v", first, repeated, next)
	}
	if n.Normalization().CachedInput != "complete" {
		t.Fatal(n.Normalization())
	}
}

func TestUnsupportedUsageAndLegacyCoverageStayUnknown(t *testing.T) {
	for _, dialect := range []string{DialectMuseJSONL, "opencode"} {
		n := NewUsageNormalizer("", nil)
		u, ok := n.Observe(&Declaration{Dialect: dialect}, `{"type":"result","usage":{"input_tokens":100,"output_tokens":10}}`)
		if ok || u.Total() != 0 || n.Normalization().Input != "unknown" {
			t.Fatalf("%s: %#v %#v", dialect, u, n.Normalization())
		}
	}
	var legacy UsageNormalization
	if legacy.Version != 0 {
		t.Fatal(legacy)
	}
}

func TestUsageCoverageMergesWithoutUpgradingLegacyOrPartial(t *testing.T) {
	var combined UsageNormalization
	known := UsageNormalization{Version: 1, Source: "harness-reported", Input: "complete", CachedInput: "unknown", Output: "complete", Cost: "unknown"}
	combined.Merge(known)
	if combined != known {
		t.Fatal(combined)
	}
	other := known
	other.Input = "unknown"
	combined.Merge(other)
	if combined.Input != "partial" || combined.CachedInput != "unknown" {
		t.Fatal(combined)
	}
	combined.MarkPartial("failed")
	if combined.Output != "partial" || combined.Cost != "unknown" {
		t.Fatal(combined)
	}
}

func claudeCostLine(session string, total float64) string {
	return fmt.Sprintf(`{"type":"result","session_id":%q,"usage":{"input_tokens":1,"output_tokens":1},"total_cost_usd":%v}`, session, total)
}

// total_cost_usd is session-cumulative: a resumed turn's cost is the step from
// the previous turn's total, never the whole total again.
func TestClaudeTurnCostIsTheStepFromTheSavedSessionTotal(t *testing.T) {
	d := &Declaration{Dialect: DialectClaudeStreamJSON}
	first := NewUsageNormalizer("", nil)
	u1, _ := first.Observe(d, claudeCostLine("s1", 3.0))
	checkpoint := first.Baseline()
	if u1.CostUSD != 3.0 || checkpoint == nil || checkpoint.ThreadID != "s1" || checkpoint.Cost == nil || *checkpoint.Cost != 3.0 {
		t.Fatalf("first turn: %#v %#v", u1, checkpoint)
	}
	second := NewUsageNormalizer("s1", checkpoint)
	u2, _ := second.Observe(d, claudeCostLine("s1", 3.5))
	if u2.CostUSD != 0.5 || second.Normalization().Cost != "complete" {
		t.Fatalf("second turn: %#v %#v", u2, second.Normalization())
	}
	if *second.Baseline().Cost != 3.5 {
		t.Fatal(second.Baseline())
	}
}

func TestClaudeCostWithoutABaselineOrAfterAResetIsUnknownNotTheWholeTotal(t *testing.T) {
	d := &Declaration{Dialect: DialectClaudeStreamJSON}
	resumed := NewUsageNormalizer("s1", nil)
	u, _ := resumed.Observe(d, claudeCostLine("s1", 160))
	if u.CostUSD != 0 || resumed.Normalization().Cost != "unknown" {
		t.Fatalf("no baseline: %#v %#v", u, resumed.Normalization())
	}
	cost := 10.0
	reset := NewUsageNormalizer("s1", &UsageBaseline{Version: 1, ThreadID: "s1", Cost: &cost})
	u, _ = reset.Observe(d, claudeCostLine("s1", 2))
	if u.CostUSD != 0 || reset.Normalization().Cost != "unknown" || *reset.Baseline().Cost != 2 {
		t.Fatalf("counter reset: %#v %#v", u, reset.Normalization())
	}
}
