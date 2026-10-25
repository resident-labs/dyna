package workeroutput

import (
	"encoding/json"
	"fmt"
	"testing"
)

func codexUsageLine(input, cached, output int64) string {
	b, _ := json.Marshal(map[string]any{"type": "turn.completed", "usage": map[string]int64{"input_tokens": input, "cached_input_tokens": cached, "output_tokens": output}})
	return string(b)
}
func observeCodex(n *UsageNormalizer, line string) Usage {
	u, _ := n.Observe(&Declaration{Dialect: DialectCodexJSONL}, line)
	return u
}
func startCodex(n *UsageNormalizer, id string) {
	observeCodex(n, `{"type":"thread.started","thread_id":"`+id+`"}`)
}

func TestCodexCumulativeNormalizationAcrossProcessesAndTurns(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	startCodex(n, "thread")
	first := observeCodex(n, codexUsageLine(100, 60, 10))
	n.BeginProcess() // recovery must retain the cumulative baseline
	second := observeCodex(n, codexUsageLine(150, 90, 14))
	repeated := observeCodex(n, codexUsageLine(150, 90, 14))
	if first.InputTokens != 100 || second.InputTokens != 50 || second.CachedTokens != 30 || second.OutputTokens != 4 || repeated.Total() != 0 {
		t.Fatalf("deltas: %#v %#v %#v", first, second, repeated)
	}
	// JSON round trip represents the checkpoint persisted across detached runners.
	b, _ := json.Marshal(n.Baseline())
	var checkpoint UsageBaseline
	if err := json.Unmarshal(b, &checkpoint); err != nil {
		t.Fatal(err)
	}
	resumed := NewUsageNormalizer("thread", &checkpoint)
	startCodex(resumed, "thread")
	delta := observeCodex(resumed, codexUsageLine(180, 110, 20))
	if delta.InputTokens != 30 || delta.CachedTokens != 20 || delta.OutputTokens != 6 || resumed.Normalization().Input != "complete" {
		t.Fatalf("resumed delta=%#v coverage=%#v", delta, resumed.Normalization())
	}
}

func TestCodexUnknownBaselineDoesNotCountHistoricalSnapshot(t *testing.T) {
	n := NewUsageNormalizer("legacy-thread", nil)
	first := observeCodex(n, codexUsageLine(10000, 9000, 100))
	if first.Total() != 0 || first.CachedTokens != 0 || n.Normalization().Input != "unknown" {
		t.Fatalf("legacy snapshot counted: %#v %#v", first, n.Normalization())
	}
	second := observeCodex(n, codexUsageLine(10100, 9080, 110))
	if second.InputTokens != 100 || second.CachedTokens != 80 || second.OutputTokens != 10 || n.Normalization().Input != "partial" {
		t.Fatalf("legacy follow-up=%#v coverage=%#v", second, n.Normalization())
	}
	// A wrong-thread checkpoint is equally untrusted.
	wrong := NewUsageNormalizer("another-thread", n.Baseline())
	if u := observeCodex(wrong, codexUsageLine(200, 100, 10)); u.Total() != 0 {
		t.Fatalf("wrong thread counted: %#v", u)
	}
}

func TestCodexCounterResetAndThreadChangeDoNotCountWholeSnapshot(t *testing.T) {
	for _, change := range []string{"reset", "thread"} {
		t.Run(change, func(t *testing.T) {
			n := NewUsageNormalizer("", nil)
			startCodex(n, "first")
			observeCodex(n, codexUsageLine(1000, 900, 100))
			if change == "thread" {
				startCodex(n, "second")
			}
			reset := observeCodex(n, codexUsageLine(100, 80, 10))
			delta := observeCodex(n, codexUsageLine(130, 100, 14))
			if reset.Total() != 0 || reset.CachedTokens != 0 || delta.InputTokens != 30 || delta.CachedTokens != 20 || n.Normalization().Input != "partial" {
				t.Fatalf("reset=%#v delta=%#v coverage=%#v", reset, delta, n.Normalization())
			}
		})
	}
}

func TestCacheCoveragePreservesMissingVersusZero(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	startCodex(n, "thread")
	observeCodex(n, `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":10}}`)
	if n.Normalization().Input != "complete" || n.Normalization().CachedInput != "unknown" {
		t.Fatal(n.Normalization())
	}
	// The first cache counter after a gap only establishes its own baseline.
	u := observeCodex(n, codexUsageLine(120, 90, 12))
	if u.InputTokens != 20 || u.CachedTokens != 0 {
		t.Fatal(u)
	}
	u = observeCodex(n, codexUsageLine(140, 90, 14))
	if u.CachedTokens != 0 || n.Normalization().CachedInput != "partial" {
		t.Fatalf("%#v %#v", u, n.Normalization())
	}
	zero := NewUsageNormalizer("", nil)
	startCodex(zero, "zero")
	observeCodex(zero, codexUsageLine(0, 0, 0))
	if zero.Normalization().Input != "complete" || zero.Normalization().CachedInput != "complete" {
		t.Fatal(zero.Normalization())
	}
}

func TestClaudeCacheReadsAreSubsetAndCostIsCumulativeAcrossProcesses(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	d := &Declaration{Dialect: DialectClaudeStreamJSON}
	line := `{"type":"result","usage":{"input_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":10,"output_tokens":5},"total_cost_usd":0.5}`
	first, _ := n.Observe(d, line)
	repeated, _ := n.Observe(d, line)
	n.BeginProcess()
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
		u, ok := n.Observe(&Declaration{Dialect: dialect}, codexUsageLine(100, 80, 10))
		if ok || u.Total() != 0 || n.Normalization().Input != "unknown" {
			t.Fatalf("%s: %#v %#v", dialect, u, n.Normalization())
		}
	}
	var legacy UsageNormalization
	if legacy.Version != 0 {
		t.Fatal(legacy)
	}
}

func TestMissingTerminalCannotCheckpointPreviousTurnsUsage(t *testing.T) {
	n := NewUsageNormalizer("", nil)
	startCodex(n, "thread")
	observeCodex(n, codexUsageLine(100, 80, 10))
	checkpoint := n.Baseline()
	n = NewUsageNormalizer("thread", checkpoint)
	if n.Baseline() != nil {
		t.Fatal("previous turn checkpoint exposed as current usage")
	}
	observeCodex(n, `{"type":"turn.completed"}`)
	u := observeCodex(n, codexUsageLine(1000, 900, 100))
	if u.Total() != 0 || n.Normalization().Input != "unknown" {
		t.Fatalf("missing terminal usage spilled into later work: %#v %#v", u, n.Normalization())
	}
	n.BeginProcess()
	if n.Baseline() != nil {
		t.Fatal("process with no usage retained a usable checkpoint")
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

func TestInvalidCumulativeCountersAreNotTrusted(t *testing.T) {
	negative := int64(-1)
	n := NewUsageNormalizer("thread", &UsageBaseline{Version: 1, ThreadID: "thread", Input: &negative})
	if u := observeCodex(n, codexUsageLine(100, 80, 10)); u.Total() != 0 {
		t.Fatal(u)
	}
	n = NewUsageNormalizer("", nil)
	startCodex(n, "new")
	u := observeCodex(n, codexUsageLine(100, 200, 10))
	if u.CachedTokens != 0 || n.Normalization().CachedInput != "unknown" {
		t.Fatalf("invalid cache ratio accepted: %#v %#v", u, n.Normalization())
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
