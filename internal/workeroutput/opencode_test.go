package workeroutput

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func openCodeSample(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/opencode-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestOpenCodeRecords(t *testing.T) {
	lines := openCodeSample(t)
	tests := []struct {
		name, line string
		want       Event
		signals    []ToolSignal
	}{
		{"step start", lines[0], Event{V: 1, Kind: "status", Name: "step", State: "started"}, []ToolSignal{{ID: "opencode-step:msg_14c27527c001dD59dQw7h08UG8", Start: true}}},
		{"completed tool", lines[1], Event{V: 1, Kind: "tool", ID: "call_14125922fb18476e873e3a56", Name: "shell", State: "completed", Input: `{"command":"echo hello"}`, Output: "hello\n"}, nil},
		{"step finish", lines[2], Event{V: 1, Kind: "status", Name: "step", State: "completed", Text: "tool-calls"}, []ToolSignal{{ID: "opencode-step:msg_14c27527c001dD59dQw7h08UG8"}}},
		{"text", lines[4], Event{V: 1, Kind: "message", Role: "assistant", Text: "done"}, nil},
		// The capture contains no reasoning or errors. These cases use native shapes.
		{"reasoning", `{"type":"reasoning","part":{"type":"reasoning","text":"Let me check."}}`, Event{V: 1, Kind: "reasoning", State: "completed", Text: "Let me check."}, nil},
		{"provider error", `{"type":"error","error":{"name":"APIError","data":{"message":"credit exhausted"}}}`, Event{V: 1, Kind: "status", Name: "error", State: "failed", Text: `{"data":{"message":"credit exhausted"},"name":"APIError"}`}, nil},
		{"string error", `{"type":"error","error":"offline"}`, Event{V: 1, Kind: "status", Name: "error", State: "failed", Text: "offline"}, nil},
		{"failed tool", `{"type":"tool_use","part":{"type":"tool","callID":"c2","tool":"read","state":{"status":"error","input":{"path":"missing"},"error":"not found"}}}`, Event{V: 1, Kind: "tool", ID: "c2", Name: "read", State: "failed", Input: `{"path":"missing"}`, Output: "not found"}, nil},
		{"structured output", `{"type":"tool_use","part":{"callID":"c3","tool":"lookup","state":{"status":"completed","input":{},"output":{"value":1}}}}`, Event{V: 1, Kind: "tool", ID: "c3", Name: "lookup", State: "completed", Input: `{}`, Output: `{"value":1}`}, nil},
	}
	decl := &Declaration{Dialect: DialectOpenCodeJSON}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeLine(decl, tt.line)
			if strings.Contains(tt.line, `"sessionID"`) && got.SessionID != "ses_eb3d8af67ffez1okScks9S1y8T" {
				t.Fatalf("session = %q", got.SessionID)
			}
			if !reflect.DeepEqual(got.Events, []Event{tt.want}) {
				t.Fatalf("events = %#v, want %#v", got.Events, tt.want)
			}
			if !reflect.DeepEqual(got.Signals, tt.signals) {
				t.Fatalf("signals = %#v, want %#v", got.Signals, tt.signals)
			}
		})
	}
	for _, line := range []string{`{"type":"future","part":{"text":"unknown"}}`, `{broken`} {
		got := NormalizeLine(decl, line)
		if len(got) != 1 || got[0].Kind != "raw" || got[0].Raw != line {
			t.Fatalf("fallback = %#v", got)
		}
	}
}

func TestOpenCodeStepBoundaries(t *testing.T) {
	lines := openCodeSample(t)
	tracker := NewToolTracker(&Declaration{Dialect: DialectOpenCodeJSON})
	if !tracker.Supported() {
		t.Fatal("step boundaries unsupported")
	}
	tests := []struct {
		line      string
		busy      int
		completed bool
	}{
		{lines[0], 1, false}, {lines[1], 1, false}, {lines[2], 0, true},
		{lines[3], 1, false}, {lines[4], 1, false},
		{`{"type":"step_finish","part":{"messageID":"msg_14c2768db001zmEQ9ONxP95eyr","reason":"stop"}}`, 0, true},
	}
	for i, tt := range tests {
		if got := tracker.Observe(tt.line); got != tt.completed || tracker.InFlight() != tt.busy {
			t.Fatalf("line %d: completed=%v busy=%d", i, got, tracker.InFlight())
		}
	}
	// Some versions omit messageID. Sequential steps must still pair correctly.
	tracker.Observe(`{"type":"step_start","part":{}}`)
	if !tracker.Observe(`{"type":"step_finish","part":{}}`) || tracker.InFlight() != 0 {
		t.Fatal("step without message ID did not close")
	}
}

func TestOpenCodeUsage(t *testing.T) {
	lines := openCodeSample(t)
	decl := &Declaration{Dialect: DialectOpenCodeJSON}
	tests := []struct {
		name     string
		lines    []string
		want     Usage
		coverage string
	}{
		{"capture", lines, Usage{InputTokens: 10080, CachedTokens: 3456, OutputTokens: 235, CostUSD: 0.0030984179999999995}, "complete"},
		{"multiple steps", []string{lines[2], `{"type":"step_finish","part":{"reason":"stop","cost":0.001,"tokens":{"input":10,"output":3,"reasoning":2,"cache":{"read":4,"write":6}}}}`}, Usage{InputTokens: 10100, CachedTokens: 3460, OutputTokens: 240, CostUSD: 0.0040984179999999995}, "complete"},
		{"zero tokens", []string{`{"type":"step_finish","part":{"cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}`}, Usage{}, "complete"},
		{"missing tokens", []string{`{"type":"step_finish","part":{}}`}, Usage{}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewUsageNormalizer("resumed-session", nil)
			var total, extracted Usage
			for _, line := range tt.lines {
				if u, ok := n.ObserveRecord(DecodeLine(decl, line)); ok {
					total.Add(u)
				}
				if u, ok := ExtractUsage(decl, line); ok {
					extracted.Add(u)
				}
			}
			if total.InputTokens != tt.want.InputTokens || total.CachedTokens != tt.want.CachedTokens || total.OutputTokens != tt.want.OutputTokens || math.Abs(total.CostUSD-tt.want.CostUSD) > 1e-12 {
				t.Fatalf("usage = %#v, want %#v", total, tt.want)
			}
			if extracted != total {
				t.Fatalf("ExtractUsage = %#v, normalizer = %#v", extracted, total)
			}
			c := n.Normalization()
			if c.Input != tt.coverage || c.CachedInput != tt.coverage || c.Output != tt.coverage || c.Cost != tt.coverage {
				t.Fatalf("coverage = %#v", c)
			}
		})
	}
}
