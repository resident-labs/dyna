package workeroutput

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAllowlistedAdaptersNormalizeRepresentedDialects(t *testing.T) {
	tests := []struct {
		name, dialect, line string
		want                Event
	}{
		{name: "text", dialect: DialectText, line: "plain answer", want: Event{V: 1, Kind: "message", Role: "assistant", Text: "plain answer"}},
		{name: "pi message", dialect: DialectPiJSON, line: `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`, want: Event{V: 1, Kind: "message", Role: "assistant", Text: "done"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeLine(&Declaration{Dialect: test.dialect}, test.line)
			if !reflect.DeepEqual(got, []Event{test.want}) {
				t.Fatalf("NormalizeLine() = %#v, want %#v", got, []Event{test.want})
			}
		})
	}
}

func TestMalformedUnsupportedAndUnknownRecordsFallBackRaw(t *testing.T) {
	for _, test := range []struct {
		name string
		decl *Declaration
		line string
	}{
		{name: "malformed", decl: &Declaration{Dialect: DialectPiJSON}, line: `{bad`},
		{name: "unsupported", decl: &Declaration{Dialect: DialectPiJSON}, line: `{"type":"future.event"}`},
		{name: "unknown future dialect", decl: &Declaration{Dialect: "future-v2"}, line: `{"anything":true}`},
		{name: "undeclared", line: "plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeLine(test.decl, test.line)
			want := []Event{{V: 1, Kind: "raw", Raw: test.line}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("NormalizeLine() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestNormalizerWritesVersionedJSONL(t *testing.T) {
	var out bytes.Buffer
	n := NewNormalizer(&Declaration{Dialect: DialectClaudeStreamJSON}, &out)
	if err := n.WriteLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.V != SchemaVersion || event.Kind != "message" || event.Text != "hello" {
		t.Fatalf("event = %#v", event)
	}
}

func TestPairToolsFoldsCompletionsIntoStarts(t *testing.T) {
	zero := 0
	events := []Event{
		{V: 1, Kind: "tool", Name: "command", State: "started", ID: "a", Input: "make", TS: 1000},
		{V: 1, Kind: "message", Role: "assistant", Text: "waiting", TS: 1500},
		{V: 1, Kind: "tool", Name: "command", State: "completed", ID: "a", Output: "ok", Exit: &zero, TS: 13000},
		{V: 1, Kind: "tool", Name: "tool_result", State: "completed", ID: "orphan", Output: "x", TS: 14000},
	}
	got, ended := PairTools(events)
	if len(got) != 3 || got[0].State != "completed" || got[0].Output != "ok" || *got[0].Exit != 0 || ended[0] != 13000 {
		t.Fatalf("paired = %#v ended=%v", got, ended)
	}
	if got[2].ID != "orphan" {
		t.Fatalf("orphan completion dropped: %#v", got)
	}
}

func TestValidateDeclarationUsesRegistry(t *testing.T) {
	if err := ValidateDeclaration(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaration(&Declaration{Dialect: DialectPiJSON}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaration(&Declaration{Dialect: "shell:/tmp/adapter"}); err == nil {
		t.Fatal("arbitrary adapter declaration was accepted")
	}
}

func TestCoalesceNormalizedEventsMergesReasoningDeltas(t *testing.T) {
	events := []Event{
		{V: 1, Kind: "reasoning", State: "delta", Text: "I will run"},
		{V: 1, Kind: "reasoning", State: "delta", Text: " the command."},
		{V: 1, Kind: "status", Name: "reasoning", State: "completed"},
		{V: 1, Kind: "message", Role: "assistant", Text: "Done."},
	}
	got := CoalesceNormalizedEvents(events)
	if len(got) != 2 || got[0].Kind != "reasoning" || got[0].State != "completed" || got[0].Text != "I will run the command." {
		t.Fatalf("coalesced events = %#v", got)
	}
	if len(events) != 4 || events[0].Text != "I will run" || events[0].State != "delta" {
		t.Fatalf("source events were mutated: %#v", events)
	}
}

// A harness that streams a reply a few tokens per record leaves delta events.
// The joiner must turn those deltas into one whole message so a packet lists
// complete messages, not the word fragments the provider streamed.
func TestCoalesceNormalizedEventsJoinsReplyDeltas(t *testing.T) {
	events := []Event{
		{V: SchemaVersion, Kind: "message", Role: "assistant", State: "delta", Text: "and"},
		{V: SchemaVersion, Kind: "message", Role: "assistant", State: "delta", Text: " run"},
		{V: SchemaVersion, Kind: "message", Role: "assistant", State: "delta", Text: " the"},
		{V: SchemaVersion, Kind: "message", Role: "assistant", State: "delta", Text: " watcher."},
	}
	got := CoalesceNormalizedEvents(events)
	if len(got) != 1 || got[0].Kind != "message" || got[0].Text != "and run the watcher." {
		t.Fatalf("joined deltas = %#v, want one whole message", got)
	}
}

func replayToolBoundaries(t *testing.T, dialect, fixture string) (peak int, final int, completions int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewToolTracker(&Declaration{Dialect: dialect})
	if !tracker.Supported() {
		t.Fatalf("%s should expose tool boundaries", dialect)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if tracker.Observe(line) {
			completions++
		}
		peak = max(peak, tracker.InFlight())
	}
	return peak, tracker.InFlight(), completions
}

func TestToolTrackerFollowsEachDialect(t *testing.T) {
	tests := []struct {
		dialect, fixture string
		peak, completed  int
	}{
		// A completion arrives only with its result record: it never opens a call.
		{DialectClaudeStreamJSON, "claude-tools.jsonl", 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			peak, final, completed := replayToolBoundaries(t, tt.dialect, tt.fixture)
			if peak != tt.peak || final != 0 || completed != tt.completed {
				t.Fatalf("peak=%d final=%d completed=%d, want peak=%d final=0 completed=%d", peak, final, completed, tt.peak, tt.completed)
			}
		})
	}
}

func TestToolBoundariesUnsupportedDialectsReportNothingInFlight(t *testing.T) {
	for _, decl := range []*Declaration{nil, {Dialect: DialectText}} {
		tracker := NewToolTracker(decl)
		if tracker.Supported() {
			t.Fatalf("%#v must not claim tool boundaries", decl)
		}
		if tracker.Observe(`{"type":"future.event"}`) || tracker.InFlight() != 0 {
			t.Fatalf("%#v tracked a tool", decl)
		}
	}
}

func TestClaudeStreamAdapterAndResult(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decl := &Declaration{Dialect: DialectClaudeStreamJSON}
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		events = append(events, NormalizeLine(decl, line)...)
	}
	var tools []string
	for _, e := range events {
		if e.Kind == "raw" {
			t.Fatalf("claude record fell back to raw: %#v", e)
		}
		if e.Kind == "tool" {
			tools = append(tools, e.Name+":"+e.State)
		}
	}
	if strings.Join(tools, ",") != "Bash:started,Read:started,tool_result:completed,tool_result:completed" {
		t.Fatalf("tool events = %v", tools)
	}
	result, session := ClaudeStreamResult(string(b))
	if result != "All done." || session != "claude-session" {
		t.Fatalf("ClaudeStreamResult = %q, %q", result, session)
	}
}

func TestExtractUsage(t *testing.T) {
	tests := []struct {
		dialect, fixture string
		want             Usage
	}{
		{DialectClaudeStreamJSON, "claude-tools.jsonl", Usage{InputTokens: 100, CachedTokens: 90, OutputTokens: 5, CostUSD: 0.01}},
	}
	for _, tt := range tests {
		b, err := os.ReadFile(filepath.Join("testdata", tt.fixture))
		if err != nil {
			t.Fatal(err)
		}
		var total Usage
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if u, ok := ExtractUsage(&Declaration{Dialect: tt.dialect}, line); ok {
				total.Add(u)
			}
		}
		if total != tt.want {
			t.Fatalf("%s usage = %#v, want %#v", tt.dialect, total, tt.want)
		}
	}
}

func TestClaudePlumbingRecordsAreStatus(t *testing.T) {
	decl := &Declaration{Dialect: DialectClaudeStreamJSON}
	for _, line := range []string{
		`{"type":"system","subtype":"commands_changed","commands":[]}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
	} {
		got := NormalizeLine(decl, line)
		if len(got) != 1 || got[0].Kind != "status" {
			t.Fatalf("%s → %#v", line, got)
		}
	}
}
