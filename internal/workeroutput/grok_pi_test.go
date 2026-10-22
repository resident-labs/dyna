package workeroutput

import (
	"os"
	"strings"
	"testing"
)

func TestGrokPiNativeFixtures(t *testing.T) {
	for _, tc := range []struct {
		dialect, file         string
		input, cached, tokens int64
	}{
		{DialectGrokJSON, "grok-acp.jsonl", 812, 0, 45},
		{DialectPiJSON, "pi-stream.jsonl", 112, 10, 5},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			stdout := string(b)
			n := NewUsageNormalizer("", nil)
			var total Usage
			for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
				r := DecodeLine(&Declaration{Dialect: tc.dialect}, line)
				if !r.Recognized() {
					t.Fatalf("unrecognized vendor example: %s", line)
				}
				if u, ok := n.ObserveRecord(r); ok {
					total.Add(u)
				}
			}
			if total.InputTokens != tc.input || total.CachedTokens != tc.cached || total.OutputTokens != tc.tokens {
				t.Fatalf("usage=%+v", total)
			}
			if tc.dialect == DialectPiJSON && total.CostUSD != 0.033 {
				t.Fatalf("cost=%v", total.CostUSD)
			}
			for _, line := range []string{"{bad", `{"type":"future","data":"keep"}`} {
				r := DecodeLine(&Declaration{Dialect: tc.dialect}, line)
				if r.Recognized() || len(r.Events) != 1 || r.Events[0].Raw != line {
					t.Fatalf("raw lost: %+v", r)
				}
			}
		})
	}
}

func TestPiProtocolFailuresAndRecovery(t *testing.T) {
	failure := `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"provider unavailable"}}`
	success := `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"recovered"}]}}`
	if PiResultError(failure) == nil || PiResultError(failure+"\n"+success) != nil {
		t.Fatal("Pi retry result incorrect")
	}
	if PiResultError(success+"\n"+`{"type":"agent_settled","aborted":true}`) == nil {
		t.Fatal("Pi abort ignored")
	}
	if got := PiOutput(success + "\n" + failure); got != "" {
		t.Fatalf("stale Pi text=%q", got)
	}
}

func TestPiFailedToolBoundaries(t *testing.T) {
	decl := &Declaration{Dialect: DialectPiJSON}
	tracker := NewToolTracker(decl)
	tracker.ObserveSignals(DecodeLine(decl, `{"type":"tool_execution_start","toolCallId":"a","toolName":"bash"}`).Signals)
	r := DecodeLine(decl, `{"type":"tool_execution_end","toolCallId":"a","toolName":"bash","isError":true,"result":{"content":[]}}`)
	if !tracker.ObserveSignals(r.Signals) || len(r.Events) != 1 || r.Events[0].State != "failed" {
		t.Fatalf("failed completion=%+v", r)
	}
}
