package workeroutput

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		{name: "codex message", dialect: DialectCodexJSONL, line: `{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"done"}}`, want: Event{V: 1, Kind: "message", Role: "assistant", Text: "done"}},
		{name: "opencode text", dialect: DialectOpenCodeJSON, line: `{"type":"text","part":{"messageID":"m","text":"answer"}}`, want: Event{V: 1, Kind: "message", Role: "assistant", Text: "answer"}},
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
		{name: "malformed", decl: &Declaration{Dialect: DialectCodexJSONL}, line: `{bad`},
		{name: "unsupported", decl: &Declaration{Dialect: DialectCodexJSONL}, line: `{"type":"future.event"}`},
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

func TestCodexAdapterNormalizesReasoningToolsAndFailures(t *testing.T) {
	tests := []struct {
		name, line string
		want       Event
	}{
		{
			name: "reasoning",
			line: `{"type":"item.completed","item":{"id":"r","type":"reasoning","text":"checking files"}}`,
			want: Event{V: 1, Kind: "reasoning", State: "completed", Text: "checking files"},
		},
		{
			name: "mcp tool",
			line: `{"type":"item.started","item":{"id":"m","type":"mcp_tool_call","server":"docs","tool":"search","arguments":{"q":"api"}}}`,
			want: Event{V: 1, Kind: "tool", Name: "docs/search", State: "started", ID: "m", Input: `{"q":"api"}`},
		},
		{
			name: "web search",
			line: `{"type":"item.started","item":{"id":"w","type":"web_search","query":"dyna output"}}`,
			want: Event{V: 1, Kind: "tool", Name: "web_search", State: "started", ID: "w", Input: "dyna output"},
		},
		{
			name: "turn failed",
			line: `{"type":"turn.failed","error":{"message":"rate limited"}}`,
			want: Event{V: 1, Kind: "status", Name: "turn", State: "failed", Text: "rate limited"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeLine(&Declaration{Dialect: DialectCodexJSONL}, test.line)
			if !reflect.DeepEqual(got, []Event{test.want}) {
				t.Fatalf("NormalizeLine() = %#v, want %#v", got, []Event{test.want})
			}
		})
	}
}

func TestToolCompletionsKeptOnlyWhenPairable(t *testing.T) {
	// Codex completions carry the item id (and exit code) and are kept so a
	// reader can fold them into the start; Cursor completions have no id and
	// are still dropped.
	got := NormalizeLine(&Declaration{Dialect: DialectCodexJSONL},
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","command":"go test ./...","aggregated_output":"FAIL","exit_code":1,"status":"failed"}}`)
	if len(got) != 1 || got[0].ID != "c" || got[0].State != "failed" || got[0].Exit == nil || *got[0].Exit != 1 {
		t.Fatalf("codex completion = %#v", got)
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
	if err := ValidateDeclaration(&Declaration{Dialect: DialectCodexJSONL}); err != nil {
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

// Grok streams a reply a few tokens per text record. The joiner must turn those
// deltas into one whole message so a packet lists complete messages, not the
// word fragments the provider streamed.
func TestCoalesceNormalizedEventsJoinsGrokReplyDeltas(t *testing.T) {
	decl := &Declaration{Dialect: DialectGrokJSON}
	var events []Event
	for _, line := range []string{
		`{"type":"text","data":"and"}`,
		`{"type":"text","data":" run"}`,
		`{"type":"text","data":" the"}`,
		`{"type":"text","data":" watcher."}`,
	} {
		events = append(events, NormalizeLine(decl, line)...)
	}
	got := CoalesceNormalizedEvents(events)
	if len(got) != 1 || got[0].Kind != "message" || got[0].Text != "and run the watcher." {
		t.Fatalf("joined grok deltas = %#v, want one whole message", got)
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
		// codex file_change arrives only as item.completed: it never opens a call.
		{DialectCodexJSONL, "codex-tools.jsonl", 2, 2},
		{DialectClaudeStreamJSON, "claude-tools.jsonl", 2, 2},
		{DialectMuseJSONL, "muse-stream.jsonl", 1, 1},
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
		if tracker.Observe(`{"type":"item.started","item":{"id":"x","type":"command_execution"}}`) || tracker.InFlight() != 0 {
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

func TestMuseFixtureNormalizesWithoutRaw(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "muse-stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decl := &Declaration{Dialect: DialectMuseJSONL}
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		events = append(events, NormalizeLine(decl, line)...)
	}
	var tools, messages []string
	for _, e := range events {
		if e.Kind == "raw" {
			t.Fatalf("muse record fell back to raw: %#v", e)
		}
		switch e.Kind {
		case "tool":
			tools = append(tools, e.Name+":"+e.State)
		case "message":
			messages = append(messages, e.Role+":"+e.Text)
		}
	}
	if !strings.Contains(strings.Join(tools, ","), "bash:started") || !strings.Contains(strings.Join(tools, ","), "bash:completed") {
		t.Fatalf("tool events = %v, want a bash started/completed pair", tools)
	}
	joined := strings.Join(messages, "|")
	if !strings.Contains(joined, "user:") || !strings.Contains(joined, "assistant:PROBE-DONE") {
		t.Fatalf("message events = %v, want the prompt and PROBE-DONE", messages)
	}
	result, session := MuseStreamResult(string(b))
	if result != "PROBE-DONE" || session != "01a132d4-6dc9-7270-b7b4-dd68fff9b304" {
		t.Fatalf("MuseStreamResult = %q, %q", result, session)
	}
}

// TestMuseToolsFixturePairsAndCarriesInputs replays a real `muse exec
// --json` capture. Muse's stream never reports a tool's input directly:
// the start record knows only the tool's name, and the input (shell
// command, file path) echoes back inside the tool result text. Starts and
// results also use different ids (task id vs call id) unless the adapter
// starts the call at the side_effect_intent record, which carries the
// call id. The normalized events must pair into one event per call, with
// the input recovered from the result text.
func TestMuseToolsFixturePairsAndCarriesInputs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "muse-tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	decl := &Declaration{Dialect: DialectMuseJSONL}
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		events = append(events, NormalizeLine(decl, line)...)
	}
	for _, e := range events {
		if e.Kind == "raw" {
			t.Fatalf("muse record fell back to raw: %#v", e)
		}
	}
	paired, _ := PairTools(events)
	var tools []Event
	for _, e := range paired {
		if e.Kind == "tool" {
			tools = append(tools, e)
		}
	}
	var names []string
	for _, e := range tools {
		if e.State != "completed" {
			t.Fatalf("tool %s state = %q, want completed (unpaired start?)", e.Name, e.State)
		}
		names = append(names, e.Name)
	}
	if want := "read_file,read_file,bash,search,edit_file,write_file"; strings.Join(names, ",") != want {
		t.Fatalf("paired tools = %v, want %s", names, want)
	}
	if !strings.Contains(tools[0].Input, "a.txt") || !strings.Contains(tools[1].Input, "b.txt") {
		t.Fatalf("read inputs = %q, %q, want the file paths", tools[0].Input, tools[1].Input)
	}
	bash := tools[2]
	if !strings.Contains(bash.Input, `"command":"echo hello-probe"`) {
		t.Fatalf("bash input = %q, want the command JSON", bash.Input)
	}
	if bash.Exit == nil || *bash.Exit != 0 {
		t.Fatalf("bash exit = %v, want 0", bash.Exit)
	}
	if bash.Output != "hello-probe\n" {
		t.Fatalf("bash output = %q, want the command output, not the result envelope", bash.Output)
	}
	if !strings.Contains(tools[5].Input, "c.txt") {
		t.Fatalf("write input = %q, want the file path", tools[5].Input)
	}
}

// TestPairToolsFallsBackToOldestSameNameStart covers artifacts written
// before the muse adapter keyed starts and results by the same id: a
// completion whose id matches no start folds into the oldest still-open
// start with the same tool name, and never into a different tool.
func TestPairToolsFallsBackToOldestSameNameStart(t *testing.T) {
	events := []Event{
		{V: 1, Kind: "tool", Name: "bash", State: "started", ID: "task-1", TS: 1000},
		{V: 1, Kind: "tool", Name: "bash", State: "started", ID: "task-2", TS: 2000},
		{V: 1, Kind: "tool", Name: "bash", State: "completed", ID: "call-1", Output: "one", TS: 3000},
		{V: 1, Kind: "tool", Name: "read_file", State: "completed", ID: "call-2", Output: "two", TS: 4000},
		{V: 1, Kind: "tool", Name: "bash", State: "completed", ID: "call-3", Output: "three", TS: 5000},
	}
	got, ended := PairTools(events)
	if len(got) != 3 {
		t.Fatalf("paired = %#v, want 3 events", got)
	}
	if got[0].Output != "one" || got[0].State != "completed" || ended[0] != 3000 {
		t.Fatalf("first bash = %#v ended=%v, want folded with output one", got[0], ended)
	}
	if got[1].Output != "three" || got[1].State != "completed" {
		t.Fatalf("second bash = %#v, want folded with output three", got[1])
	}
	if got[2].Name != "read_file" || got[2].Output != "two" {
		t.Fatalf("read_file completion folded into a bash start: %#v", got)
	}
}

func TestMuseUnknownRecordFallsBackRaw(t *testing.T) {
	got := NormalizeLine(&Declaration{Dialect: DialectMuseJSONL}, `{"payload_type":"future.record","payload":{}}`)
	if len(got) != 1 || got[0].Kind != "raw" {
		t.Fatalf("unknown muse record = %#v, want one raw event", got)
	}
}

func TestExtractUsage(t *testing.T) {
	tests := []struct {
		dialect, fixture string
		want             Usage
	}{
		{DialectCodexJSONL, "codex-tools.jsonl", Usage{InputTokens: 120, CachedTokens: 20, OutputTokens: 30}},
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

func TestMuseDeltaPreservesWhitespace(t *testing.T) {
	for _, text := range []string{" ", "\n\n", "word"} {
		line := fmt.Sprintf(`{"payload_type":"run.output.delta","payload":{"text":%q}}`, text)
		events := NormalizeLine(&Declaration{Dialect: DialectMuseJSONL}, line)
		if len(events) != 1 || events[0].State != "delta" || events[0].Text != text {
			t.Errorf("delta %q = %#v", text, events)
		}
	}
}
