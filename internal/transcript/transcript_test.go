package transcript

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func ev(kind, name, state, input string) workeroutput.Event {
	return workeroutput.Event{V: 1, Kind: kind, Name: name, State: state, Input: input}
}

func TestUnwrapShellAndSummaries(t *testing.T) {
	cases := []struct {
		e            workeroutput.Event
		verb, target string
	}{
		{ev("tool", "command", "started", `dyna post @parent "Done"`), "messaged", "@parent"},
		{ev("tool", "command", "started", `dyna post -h`), "ran", "dyna post -h"},
		{ev("tool", "command", "started", `dyna run --help`), "ran", "dyna run --help"},
		{ev("tool", "command", "started", `dyna post @parent -- --help`), "messaged", "@parent"},
		{ev("tool", "command", "started", `dyna post @parent "Use --help to see usage"`), "messaged", "@parent"},
		{ev("tool", "command", "started", `dyna post @parent 'Use -h to see usage'`), "messaged", "@parent"},
		{ev("tool", "command", "started", `dyna post @parent "Done"; dyna run --help`), "messaged", "@parent"},
		{ev("tool", "command", "started", `dyna post "--help"`), "ran", `dyna post "--help"`},
		{ev("tool", "command", "started", `dyna msg @parent "Done"`), "messaged", "@parent"},
		{ev("tool", "command", "started", `"$DYNA_BIN" post @parent "Done"`), "messaged", "@parent"},
		{ev("tool", "command", "started", `/usr/bin/bash -lc 'go test ./...'`), "ran", "go test ./..."},
		{ev("tool", "command", "started", `/usr/bin/bash -lc "rg -n \"frame\" ."`), "ran", `rg -n "frame" .`},
		{ev("tool", "command", "started", `/bin/zsh -lc "dyna journal 'Read the brief' --kind update"`), "journaled", "Read the brief"},
		{ev("tool", "command", "started", `bash -lc 'dyna subagent start --profile sol --name cu_web "build it"'`), "started a subagent", "cu_web"},
		{ev("tool", "command", "started", `bash -lc "ssh ci@build-host 'nvidia-smi; df -h'"`), "ran on build-host", "nvidia-smi; df -h"},
		{ev("tool", "Bash", "started", `{"command":"make release","description":"Build"}`), "ran", "make release"},
		{ev("tool", "Read", "started", `{"file_path":"/repo/internal/decoder/decoder.go"}`), "read", "decoder/decoder.go"},
		{ev("tool", "Edit", "started", `{"file_path":"/repo/a.go","old_string":"x"}`), "edited", "repo/a.go"},
		{ev("tool", "Grep", "started", `{"pattern":"frameCount"}`), "searched for", "frameCount"},
		{ev("tool", "web_search", "started", "dyna output"), "searched the web", "dyna output"},
		{ev("tool", "docs/search", "started", `{"q":"x"}`), "called", "docs/search"},
		{ev("tool", "mcp__dyna__events_wait", "started", `{}`), "called", "dyna/events_wait"},
		{ev("tool", "mcp__dyna__shell", "started", `{"command":"go test ./...","description":"Run the tests"}`), "ran", "go test ./..."},
		{ev("tool", "dyna_shell", "started", `{"command":"make"}`), "ran", "make"},
		{ev("tool", "dyna.shell", "started", `{"command":"make"}`), "ran", "make"},
		{ev("tool", "mcp__dyna__shell_output", "started", `{"job_id":"sh_1"}`), "called", "dyna/shell_output"},
		{ev("tool", "Task", "started", `{"description":"audit the parser"}`), "delegated to a subagent", "audit the parser"},
	}
	for _, c := range cases {
		got := Summarize(c.e)
		if got.Verb != c.verb || got.Target != c.target {
			t.Errorf("%s %q → %q %q, want %q %q", c.e.Name, c.e.Input, got.Verb, got.Target, c.verb, c.target)
		}
	}
	fc := Summarize(ev("tool", "file_change", "started", "update /r/a.go\nadd /r/b.go\nupdate /r/c.go"))
	if Headline(fc) != "edited 3 files (r/a.go, r/b.go, r/c.go)" {
		t.Fatalf("file_change headline %q", Headline(fc))
	}
}

func TestFromEventsCondensesAndPairs(t *testing.T) {
	zero, one := 0, 1
	events := []workeroutput.Event{
		{V: 1, Kind: "status", Name: "thread", State: "started"},
		{V: 1, Kind: "message", Role: "user", Text: "echoed prompt"},
		{V: 1, Kind: "reasoning", State: "completed", Text: "**Planning the fix**\n\ndetails"},
		{V: 1, Kind: "tool", Name: "command", State: "started", ID: "a", Input: "go test ./...", TS: 1000},
		{V: 1, Kind: "tool", Name: "command", State: "completed", ID: "a", Output: "ok", Exit: &zero, TS: 13000},
		{V: 1, Kind: "tool", Name: "Read", State: "started", Input: `{"file_path":"/x/a.go"}`},
		{V: 1, Kind: "tool", Name: "Read", State: "started", Input: `{"file_path":"/x/b.go"}`},
		{V: 1, Kind: "tool", Name: "command", State: "started", ID: "b", Input: "false", TS: 14000},
		{V: 1, Kind: "tool", Name: "command", State: "failed", ID: "b", Exit: &one, TS: 15000},
		{V: 1, Kind: "raw", Raw: `{"weird":true}`},
		{V: 1, Kind: "status", Name: "error", State: "failed", Text: "Under-development features enabled: code_mode"},
		{V: 1, Kind: "message", Role: "assistant", Text: "Done."},
	}
	items, hidden := FromEvents(events)
	var got []string
	for _, it := range items {
		switch it.Kind {
		case ToolUse:
			got = append(got, Headline(it.Tool)+" "+it.Tool.Target+" "+ToolAside(it.Tool))
		case Thought, Reply:
			got = append(got, it.Text)
		}
	}
	want := []string{
		"Planning the fix",
		"ran go test ./... (12s, exit 0)",
		"read 2 files (x/a.go, x/b.go) x/a.go ",
		"ran false (1s, exit 1)",
		"Done.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") || hidden != 2 {
		t.Fatalf("items = %q hidden=%d", got, hidden)
	}
}

func TestFromEventsHidesHookTrustStartupWarning(t *testing.T) {
	warning := "`--dangerously-bypass-hook-trust` is enabled. Enabled hooks may run without review for this invocation."
	for _, tc := range []struct {
		name   string
		events []workeroutput.Event
		hidden int
		kinds  []Kind
	}{
		{
			name: "repeated startup warnings",
			events: []workeroutput.Event{
				{Kind: "status", Name: "error", State: "failed", Text: warning},
				{Kind: "status", Name: "error", State: "failed", Text: "\n" + warning + "\n"},
			},
			hidden: 2,
		},
		{
			name: "task error",
			events: []workeroutput.Event{
				{Kind: "status", Name: "error", State: "failed", Text: "Hook failed: exit status 1"},
			},
			kinds: []Kind{Error},
		},
		{
			name: "warning with additional failure",
			events: []workeroutput.Event{
				{Kind: "status", Name: "error", State: "failed", Text: warning + "\nHook failed: exit status 1"},
			},
			kinds: []Kind{Error},
		},
		{
			name: "agent discusses warning",
			events: []workeroutput.Event{
				{Kind: "message", Role: "assistant", Text: warning},
			},
			kinds: []Kind{Reply},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, hidden := FromEvents(tc.events)
			if hidden != tc.hidden || len(items) != len(tc.kinds) {
				t.Fatalf("items = %+v, hidden = %d; want %d items, hidden = %d", items, hidden, len(tc.kinds), tc.hidden)
			}
			for i, kind := range tc.kinds {
				if items[i].Kind != kind || items[i].Text != tc.events[i].Text {
					t.Fatalf("item = %+v; want kind %v, text %q", items[i], kind, tc.events[i].Text)
				}
			}
		})
	}
}

func TestSessionTurnPlacesAnchoredSteeringAndMarksMissingPosition(t *testing.T) {
	base := time.UnixMilli(1_791_000_000_000)
	anchor := int64(20)
	turn := subagent.Turn{
		N: 2, Prompt: "[dyna message msg_1 from amber-otter-42:lead]\nTo: amber-otter-42:decoder (you)\nReply: dyna post @amber-otter-42:lead \"…\"\n\nFix the decoder", Status: subagent.TurnOK,
		Created: base, Started: base, Ended: base.Add(time.Minute),
		Steering:   []subagent.Steer{{TS: base.Add(20 * time.Second).UnixMilli(), Message: "also run vet", Delivered: true, SourceOffset: &anchor}},
		Deliveries: []subagent.Delivery{{Kind: "steering", Outcome: "boundary", WaitedMs: 31000}},
	}
	timed := []workeroutput.Event{
		{V: 1, Kind: "tool", Name: "command", State: "started", Input: "go test ./...", TS: base.Add(5 * time.Second).UnixMilli()},
		{V: 1, Kind: "message", Role: "assistant", Text: "Tests pass; vet is clean.", TS: base.Add(50 * time.Second).UnixMilli(), SourceOffset: 20},
	}
	tr := SessionTurn(turn, timed, false)
	kinds := ""
	for _, it := range tr.Items {
		kinds += map[Kind]string{Prompt: "P", Steering: "S", Reply: "R", ToolUse: "T"}[it.Kind]
	}
	if kinds != "PTSR" {
		t.Fatalf("order = %s", kinds)
	}
	if tr.Items[0].From != "amber-otter-42:lead" || tr.Items[0].Text != "Fix the decoder" {
		t.Fatalf("prompt = %+v", tr.Items[0])
	}
	if s := tr.Items[2]; !s.Delivered || s.Status != "delivered at a tool boundary after 31s" {
		t.Fatalf("steering = %+v", s)
	}
	// Missing anchor evidence is explicit; timestamps and continuations do not
	// invent a position.
	turn.Steering[0].SourceOffset = nil
	untimed := []workeroutput.Event{
		{V: 1, Kind: "status", Name: "thread", State: "started"},
		{V: 1, Kind: "tool", Name: "command", State: "started", Input: "go test ./..."},
		{V: 1, Kind: "status", Name: "thread", State: "started"},
		{V: 1, Kind: "message", Role: "assistant", Text: "done"},
	}
	tr = SessionTurn(turn, untimed, false)
	kinds = ""
	for _, it := range tr.Items {
		kinds += map[Kind]string{Prompt: "P", Steering: "S", Reply: "R", ToolUse: "T"}[it.Kind]
	}
	if kinds != "PTRS" || !strings.Contains(tr.Items[3].Status, "source position unavailable") {
		t.Fatalf("missing position order = %s, items = %+v", kinds, tr.Items)
	}
	// A turn with a JSON result and no reply events shows the result.
	turn.Result = map[string]any{"ok": true}
	tr = SessionTurn(turn, nil, false)
	if last := tr.Items[len(tr.Items)-1]; last.Kind != Reply || !strings.Contains(last.Text, "```json") {
		t.Fatalf("result fallback = %+v", last)
	}
}

func TestDeliveryTextAndActivity(t *testing.T) {
	if got := DeliveryText(false, nil, false); !strings.Contains(got, "queued") {
		t.Fatal(got)
	}
	if got := DeliveryText(false, nil, true); !strings.HasPrefix(got, "not delivered") {
		t.Fatal(got)
	}
	if got := DeliveryText(true, &subagent.Delivery{Outcome: "bound-hit", WaitedMs: 120000}, true); !strings.Contains(got, "interrupted") {
		t.Fatal(got)
	}
	run := Turn{Status: subagent.TurnRunning, Items: []Item{{Kind: Prompt}, {Kind: ToolUse, Tool: Tool{Verb: "ran", Target: "make"}}}}
	if got := Activity(run); got != "ran make" {
		t.Fatal(got)
	}
	run.Items[1].Tool.Done = true
	if got := Activity(run); got != "thinking" {
		t.Fatal(got)
	}
	if got := Activity(Turn{Status: subagent.TurnQueued}); !strings.Contains(got, "preparing") {
		t.Fatal(got)
	}
}

// TestMuseEventsRenderTargets covers muse-dialect events as older dyna
// versions normalized them: starts keyed by task id with no input, results
// keyed by call id whose output echoes the command or file read. The
// transcript must still show what ran and what was read, one line per
// call. Tool lines with nothing recoverable must not render blank groups
// like "read 3 files (, , , …)".
func TestMuseEventsRenderTargets(t *testing.T) {
	bashOut := "{\n  \"chunk_id\": \"exec-1-1\",\n  \"command\": \"echo hello-probe\",\n  \"description\": \"Echo probe\",\n  \"exit_code\": 0,\n  \"terminal_status\": \"completed\",\n  \"output\": \"hello-probe\\n\",\n  \"original_output_bytes\": 12,\n  \"truncated\": false\n}"
	events := []workeroutput.Event{
		{V: 1, Kind: "tool", Name: "bash", State: "started", ID: "task-1"},
		{V: 1, Kind: "tool", Name: "bash", State: "completed", ID: "call-1", Output: bashOut},
		{V: 1, Kind: "tool", Name: "read_file", State: "started", ID: "task-2"},
		{V: 1, Kind: "tool", Name: "read_file", State: "completed", ID: "call-2", Output: "Read text file `/x/a.go`.\n1|package x"},
		{V: 1, Kind: "tool", Name: "read_file", State: "started", ID: "task-3"},
		{V: 1, Kind: "tool", Name: "read_file", State: "completed", ID: "call-3", Output: "Read text file `/x/b.go`.\n1|package x"},
		{V: 1, Kind: "tool", Name: "tool", State: "completed", ID: "call-4", Output: bashOut},
	}
	items, _ := FromEvents(events)
	var got []string
	for _, it := range items {
		if it.Kind == ToolUse {
			line := Headline(it.Tool)
			if it.Tool.Count <= 1 {
				line += " " + it.Tool.Target
			}
			got = append(got, strings.TrimSpace(line)+" "+ToolAside(it.Tool))
		}
	}
	want := []string{"ran echo hello-probe (exit 0)", "read 2 files (x/a.go, x/b.go) ", "ran echo hello-probe (exit 0)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", got, want)
	}

	blank := Tool{Verb: "read", Count: 3, Targets: []string{"", "", ""}}
	if h := Headline(blank); h != "read 3 files" {
		t.Fatalf("blank group headline = %q, want %q", h, "read 3 files")
	}
}

func TestRepeatedReplyShownOnce(t *testing.T) {
	answer := "## Fixed Add\n\n- returns a + b now\n- go vet is clean"
	items, _ := FromEvents([]workeroutput.Event{
		{V: 1, Kind: "message", Role: "assistant", Text: answer},
		{V: 1, Kind: "tool", Name: "Bash", State: "started", Input: `{"command":"dyna journal 'done'"}`},
		{V: 1, Kind: "message", Role: "assistant", Text: answer},
	})
	replies := 0
	for _, it := range items {
		if it.Kind == Reply {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("replies = %d: %+v", replies, items)
	}
}

// A post that wakes an idle session is the turn's prompt. The runner also
// records it as delivered steering; the transcript shows it once.
func TestSessionTurnShowsWakingPostOnce(t *testing.T) {
	base := time.UnixMilli(1_791_000_000_000)
	frame := "[dyna message msg_9 from dyna-agent (human)]\nTo: dyna-agent:lead (you)\nReply: dyna post @dyna-agent \"…\"\n\nship it"
	later := "[dyna message msg_10 from dyna-agent (human)]\nTo: dyna-agent:lead (you)\nReply: dyna post @dyna-agent \"…\"\n\nalso run vet"
	turn := subagent.Turn{
		N: 3, Prompt: frame, Status: subagent.TurnOK, Created: base, Started: base, Ended: base.Add(time.Minute),
		Steering: []subagent.Steer{
			{ID: "msg_9", TS: base.UnixMilli(), Message: frame, Delivered: true},
			{ID: "msg_10", TS: base.Add(20 * time.Second).UnixMilli(), Message: later, Delivered: true},
		},
		Deliveries: []subagent.Delivery{{Kind: "steering", Outcome: "boundary", WaitedMs: 4000}},
	}
	tr := SessionTurn(turn, nil, false)
	var steering []Item
	for _, it := range tr.Items {
		if it.Kind == Steering {
			steering = append(steering, it)
		}
	}
	if tr.Items[0].Kind != Prompt || tr.Items[0].Text != "ship it" {
		t.Fatalf("prompt = %+v", tr.Items[0])
	}
	if len(steering) != 1 || steering[0].Text != "also run vet" {
		t.Fatalf("steering = %+v, want only the later post", steering)
	}
	if steering[0].Status != "delivered at a tool boundary after 4s; source position unavailable" {
		t.Fatalf("the live steer must keep its delivery note, got %q", steering[0].Status)
	}
}

func TestSessionTurnPreservesUsageProvenance(t *testing.T) {
	normalization := workeroutput.UsageNormalization{Version: 1, Source: "harness-reported", Input: "partial", CachedInput: "unknown", Output: "complete", Cost: "unknown"}
	got := SessionTurn(subagent.Turn{Harness: "codex", OutputDialect: workeroutput.DialectCodexJSONL, UsageNormalization: normalization}, nil, false)
	if got.Harness != "codex" || got.OutputDialect != workeroutput.DialectCodexJSONL || got.UsageNormalization != normalization {
		t.Fatalf("provenance lost: %#v", got)
	}
}

func TestAntigravityToolShownOnceOnDone(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectAntigravityStreamJSON}
	var events []workeroutput.Event
	for _, state := range []string{"ACTIVE", "DONE"} {
		line := fmt.Sprintf(`{"event":"step_update","step_update":{"step_index":1,"state":%q,"step_type":"tool","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"echo hello"},"output":"hello"}}}`, state)
		events = append(events, workeroutput.NormalizeLine(decl, line)...)
	}
	items, _ := FromEvents(events)
	if len(items) != 1 || items[0].Kind != ToolUse || !items[0].Tool.Done || items[0].Tool.Target != "echo hello" || items[0].Tool.Output != "hello" {
		t.Fatalf("tool transcript = %#v", items)
	}
}

func TestAntigravityReplyFragments(t *testing.T) {
	raw, err := os.ReadFile("testdata/antigravity-fragments.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var events []workeroutput.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		events = append(events, workeroutput.NormalizeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectAntigravityStreamJSON}, line)...)
	}
	want := "The transcript preserves words, even when a long fragment arrives in the middle of a word.\n\nA second paragraph\nkeeps its line break.\n"
	items, _ := FromEvents(events)
	if len(items) != 2 || items[0].Text != want || items[1].Text != "Next message." {
		t.Fatalf("fragment replies = %#v", items)
	}
	// The same stored records before the delta marker was introduced.
	for i := range events {
		events[i].State = strings.TrimSuffix(events[i].State, "delta")
		events[i].ID = ""
	}
	turn := SessionTurn(subagent.Turn{OutputDialect: workeroutput.DialectAntigravityStreamJSON}, events, false)
	var replies []string
	for _, item := range turn.Items {
		if item.Kind == Reply {
			replies = append(replies, item.Text)
		}
	}
	if len(replies) != 2 || replies[0] != want || replies[1] != "Next message." {
		t.Fatalf("legacy replies = %q", replies)
	}
}

func TestDistinctShortRepliesHaveParagraphBoundary(t *testing.T) {
	items, _ := FromEvents([]workeroutput.Event{
		{Kind: "message", Text: "First."},
		{Kind: "message", Text: "Second."},
	})
	if len(items) != 1 || items[0].Text != "First.\n\nSecond." {
		t.Fatalf("items = %#v", items)
	}
}

// Grok streams its reply a few tokens per record; the transcript must show one
// reply per run of text, not one line per token.
func TestGrokReplyTokensJoin(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectGrokJSON}
	var events []workeroutput.Event
	for _, line := range []string{
		`{"type":"text","data":"Goal"}`, `{"type":"text","data":" g345"}`, `{"type":"text","data":" is done."}`,
		`{"type":"tool_call","toolCallId":"t1","title":"run_terminal_command","status":"completed"}`,
		`{"type":"text","data":"This session"}`, `{"type":"text","data":" is dormant."}`,
	} {
		events = append(events, workeroutput.NormalizeLine(decl, line)...)
	}
	check := func(label string, items []Item) {
		var replies []string
		for _, item := range items {
			if item.Kind == Reply {
				replies = append(replies, item.Text)
			}
		}
		if len(replies) != 2 || replies[0] != "Goal g345 is done." || replies[1] != "This session is dormant." {
			t.Fatalf("%s replies = %q", label, replies)
		}
	}
	items, _ := FromEvents(events)
	check("live", items)
	// The same stored records before the delta marker was introduced.
	for i := range events {
		if events[i].Kind == "message" {
			events[i].State = ""
		}
	}
	check("legacy", SessionTurn(subagent.Turn{OutputDialect: workeroutput.DialectGrokJSON}, events, false).Items)
}

// Grok sends one tool_call, then tool_call_update records without the tool
// name; in_progress updates must not open more (empty) tool blocks. The
// record shapes are copied from a real Grok 1.0.62 turn.
func TestGrokToolUpdatesShowOneTool(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectGrokJSON}
	var events []workeroutput.Event
	for _, line := range []string{
		`{"type":"text","data":"On it."}`,
		`{"type":"tool_call","toolCallId":"c1","title":"run_terminal_command","kind":"execute","status":"pending","toolName":"run_terminal_command","rawInput":{"command":"echo hi","description":"Say hi"}}`,
		`{"type":"tool_call_update","toolCallId":"c1","status":null,"content":[{"type":"content","content":{"type":"text","text":"Say hi"}}],"rawOutput":null}`,
		`{"type":"tool_call_update","toolCallId":"c1","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":""}}],"rawOutput":{"type":"Bash","output":[],"exit_code":0,"command":"echo hi"}}`,
		`{"type":"tool_call_update","toolCallId":"c1","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"hi\n"}}],"rawOutput":{"type":"Bash","output":[104,105],"exit_code":0,"command":"echo hi"}}`,
		`{"type":"tool_call_update","toolCallId":"c1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"hi\n"}}],"rawOutput":{"type":"Bash","output":[104,105],"exit_code":0,"command":"echo hi"}}`,
		`{"type":"text","data":"Done."}`,
	} {
		events = append(events, workeroutput.NormalizeLine(decl, line)...)
	}
	items, _ := FromEvents(events)
	var tools []Item
	for _, item := range items {
		if item.Kind == ToolUse {
			tools = append(tools, item)
		}
	}
	if len(tools) != 1 || !tools[0].Tool.Done {
		t.Fatalf("tools = %#v", tools)
	}
	// The same turn stored before the fix: in_progress updates were starts.
	for i := range events {
		if events[i].Kind == "tool" && events[i].State == "updated" && strings.Contains(events[i].Output, "exit_code") {
			events[i].State = "started"
		}
	}
	tools = nil
	for _, item := range SessionTurn(subagent.Turn{OutputDialect: workeroutput.DialectGrokJSON}, events, false).Items {
		if item.Kind == ToolUse {
			tools = append(tools, item)
		}
	}
	if len(tools) != 1 || !tools[0].Tool.Done {
		t.Fatalf("legacy tools = %#v", tools)
	}
}

func TestCompactionIsOneTypedItem(t *testing.T) {
	msg := func(text string) workeroutput.Event {
		return workeroutput.Event{V: 1, Kind: "message", Role: "assistant", Text: text}
	}
	compaction := func(state, trigger, text string, before, after int64) workeroutput.Event {
		e := ev("status", "compaction", state, "")
		e.Text, e.Trigger, e.TokensBefore, e.TokensAfter = text, trigger, before, after
		return e
	}
	compactions := func(items []Item) (out []CompactionInfo) {
		for _, it := range items {
			if it.Kind == Compaction {
				out = append(out, it.Compaction)
			}
		}
		return out
	}
	running, _ := FromEvents([]workeroutput.Event{msg("Before."), compaction("started", "", "", 0, 0), compaction("started", "", "", 0, 0)})
	if got := compactions(running); len(got) != 1 || got[0].State != CompactionStarted {
		t.Fatalf("running = %+v, want one started item", got)
	}
	if got := Activity(Turn{Status: subagent.TurnRunning, Items: running}); got != "compacting context" {
		t.Fatalf("activity = %q", got)
	}
	items, _ := FromEvents([]workeroutput.Event{
		msg("Before."),
		compaction("started", "", "", 0, 0),
		compaction("completed", "auto", "", 122451, 14705),
		msg("After."),
		compaction("started", "", "", 0, 0),
		compaction("failed", "", "session limit", 0, 0),
		// TODO: legacy/shim remove later — Claude output written before the compaction status shape
		workeroutput.Event{V: 1, Kind: "status", Name: "compact_boundary", State: "completed"},
	})
	got := compactions(items)
	if len(got) != 3 {
		t.Fatalf("compactions = %+v, want three", got)
	}
	if got[0] != (CompactionInfo{State: CompactionCompleted, Trigger: "auto", Before: 122451, After: 14705}) {
		t.Fatalf("completed = %+v", got[0])
	}
	if got[1] != (CompactionInfo{State: CompactionFailed, Reason: "session limit"}) {
		t.Fatalf("failed = %+v", got[1])
	}
	if got[2].State != CompactionCompleted {
		t.Fatalf("legacy boundary = %+v", got[2])
	}
	// A compaction is its own item, never a note with matching words.
	for _, it := range items {
		if it.Kind == Note {
			t.Fatalf("compaction left a note: %+v", it)
		}
	}
	if items[1].Kind != Compaction || items[2].Kind != Reply {
		t.Fatalf("compaction out of place: %+v", items)
	}
}

func TestCompactTokensMatchesTheDivider(t *testing.T) {
	// The web's compactTokens must print the same string for the same size.
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{999, "999"},
		{182000, "182k"},
		{122500, "123k"},
		{41000, "41k"},
		{999499, "999k"},
		{1000000, "1.0M"},
		{1050000, "1.1M"},
	} {
		if got := CompactTokens(tc.n); got != tc.want {
			t.Errorf("CompactTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestCompactionLine(t *testing.T) {
	for _, tc := range []struct {
		c    CompactionInfo
		want string
	}{
		{CompactionInfo{State: CompactionStarted}, "Compacting context…"},
		{CompactionInfo{State: CompactionCompleted, Trigger: "auto", Before: 182000, After: 41000}, "Context compacted · 182k → 41k tokens · auto"},
		{CompactionInfo{State: CompactionCompleted, Trigger: "dyna"}, "Context compacted · dyna"},
		{CompactionInfo{State: CompactionCompleted, After: 41000}, "Context compacted · 41k tokens after"},
		{CompactionInfo{State: CompactionFailed, Reason: "session limit"}, "Context compaction failed: session limit"},
		{CompactionInfo{State: CompactionFailed}, "Context compaction failed"},
	} {
		if got := CompactionLine(tc.c); got != tc.want {
			t.Errorf("CompactionLine(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestSteeringItemsPreservePostReceipt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		items := steeringItems(subagent.Turn{Steering: []subagent.Steer{{ID: "msg_receipt", TS: 1, Message: "[dyna message msg_receipt from alpha (human)]\n\nCheck the report", Delivered: delivered}}})
		if len(items) != 1 || items[0].PostID != "msg_receipt" || items[0].Delivered != delivered {
			t.Fatalf("receipt lost: %+v", items)
		}
	}
}

func TestSessionTurnSplitsBatchedPostSenders(t *testing.T) {
	owner := "[dyna message msg_owner from alpha (human)]\nTo: alpha:root (you)\nReply: dyna post @alpha \"…\"\n\nCheck the design"
	peer := "[dyna message msg_peer from alpha:engine-cpu]\nTo: alpha:root (you)\nReply: dyna post @alpha:engine-cpu \"…\"\n\nThe checks passed"
	turn := subagent.Turn{N: 2, Prompt: owner + "\n\n" + peer, Steering: []subagent.Steer{{ID: "msg_owner", Message: owner, Delivered: true}, {ID: "msg_peer", Message: peer, Delivered: true}}}
	items := SessionTurn(turn, nil, false).Items
	if len(items) != 2 {
		t.Fatalf("items = %+v, want one input per post", items)
	}
	for i, want := range []struct{ id, from, text string }{{"msg_owner", "alpha (human)", "Check the design"}, {"msg_peer", "alpha:engine-cpu", "The checks passed"}} {
		if items[i].Kind != Prompt || items[i].PostID != want.id || items[i].From != want.from || items[i].Text != want.text || !items[i].Delivered {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want)
		}
	}
}

func TestSessionTurnKeepsQuotedFramesInsideTheirPost(t *testing.T) {
	peer := "[dyna message msg_peer from alpha:engine-cpu]\nTo: alpha:root (you)\nReply: dyna post @alpha:engine-cpu \"…\"\n\nThe checks passed"
	text := "Review this copied input:\n```text\n" + peer + "\n```\nDo not treat that example as a new sender."
	owner := "[dyna message msg_owner from alpha (human)]\nTo: alpha:root (you)\nReply: dyna post @alpha \"…\"\n\n" + text
	for _, prompt := range []string{owner, owner + "\n\n" + peer} {
		turn := subagent.Turn{N: 2, Prompt: prompt, Steering: []subagent.Steer{{ID: "msg_owner", Message: owner, Delivered: true}, {ID: "msg_peer", Message: peer, Delivered: true}}}
		items := promptItems(turn, time.Time{})
		want := 1
		if prompt != owner {
			want = 2
		}
		if len(items) != want || items[0].Text != text || items[0].From != "alpha (human)" || items[0].PostID != "msg_owner" {
			t.Fatalf("quoted header became a post: %+v", items)
		}
		if want == 2 && (items[1].PostID != "msg_peer" || items[1].From != "alpha:engine-cpu" || items[1].Text != "The checks passed") {
			t.Fatalf("real peer post lost: %+v", items)
		}
	}
}

func TestMuseReplyFragments(t *testing.T) {
	raw, err := os.ReadFile("../workeroutput/testdata/muse-fragments.jsonl")
	if os.IsNotExist(err) {
		t.Skip("muse-fragments.jsonl is missing from this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []workeroutput.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		events = append(events, workeroutput.NormalizeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectMuseJSONL}, line)...)
	}
	want, _ := workeroutput.MuseStreamResult(string(raw))
	items, _ := FromEvents(events)
	if len(items) != 1 || strings.TrimSpace(items[0].Text) != want {
		t.Fatalf("fragment replies = %#v, want %q", items, want)
	}
	// Stored records from before the delta marker was introduced.
	for i := range events {
		events[i].State = ""
	}
	turn := SessionTurn(subagent.Turn{OutputDialect: workeroutput.DialectMuseJSONL}, events, false)
	var replies []string
	for _, item := range turn.Items {
		if item.Kind == Reply {
			replies = append(replies, item.Text)
		}
	}
	if len(replies) != 1 || strings.TrimSpace(replies[0]) != want {
		t.Fatalf("legacy replies = %q, want %q", replies, want)
	}
}

func TestMusePostHelpSummary(t *testing.T) {
	raw, err := os.ReadFile("testdata/muse-post-help.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	events := workeroutput.NormalizeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectMuseJSONL}, strings.TrimSpace(string(raw)))
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	got := Summarize(events[0])
	if got.Verb != "ran" || !strings.Contains(got.Target, "post --help") {
		t.Fatalf("help summary = %#v", got)
	}
}

func TestGrokMCPShellJobs(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectGrokJSON}
	var events []workeroutput.Event
	for _, line := range []string{
		`{"type":"tool_call","toolCallId":"run","toolName":"use_tool","rawInput":{"tool_name":"dyna__shell","tool_input":{"command":"echo hello","description":"Print hello"}}}`,
		`{"type":"tool_call_update","toolCallId":"run","status":"completed","rawOutput":{"type":"MCP","tool_name":"shell","server_name":"dyna","output":{"OkayOutput":"Job sh_20261021-020815-4ce61610. Exit code 0.\nhello\n"}}}`,
		`{"type":"tool_call","toolCallId":"stop","toolName":"use_tool","rawInput":{"tool_name":"dyna__shell_stop","tool_input":{"job_id":"sh_20261021-020815-4ce61610"}}}`,
		`{"type":"tool_call_update","toolCallId":"stop","status":"completed"}`,
	} {
		events = append(events, workeroutput.NormalizeLine(decl, line)...)
	}
	paired, _ := workeroutput.PairTools(events)
	if len(paired) != 2 {
		t.Fatalf("paired = %+v", paired)
	}
	for i, op := range []string{"run", "stop"} {
		tool := Summarize(paired[i])
		if tool.JobOp != op || tool.JobID != "sh_20261021-020815-4ce61610" || !tool.Done {
			t.Fatalf("%s = %+v", op, tool)
		}
	}
	items, hidden := FromEvents(events)
	if hidden != 0 || len(items) != 2 || items[0].Tool.Description != "Print hello" || items[1].Tool.JobOp != "stop" {
		t.Fatalf("items = %+v; hidden = %d", items, hidden)
	}
}

// This completion comes from ses_20261020-014826-f8c35fee (agy 1.4.2).
func TestAntigravityMCPShellStop(t *testing.T) {
	line := `{"event":"step_update","step_update":{"conversation_id":"468b3011-b023-4519-8833-f7e74d588927","step_index":18,"state":"DONE","step_type":"tool","tool_name":"call_mcp_tool","duration_seconds":0.141773991,"tool_info":{"name":"call_mcp_tool","parameters":{"Arguments":{"job_id":"sh_20261020-014955-6adc1631"},"ServerName":"dyna-catalog_dyna","ToolName":"shell_stop"},"output":"Exit code -1 (terminated).\n\n[dyna: stop requested; sending SIGTERM to the process group]\n"}}}`
	events := workeroutput.NormalizeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectAntigravityStreamJSON}, line)
	if len(events) != 1 || events[0].Name != "dyna__shell_stop" || events[0].Input != `{"job_id":"sh_20261020-014955-6adc1631"}` {
		t.Fatalf("events = %+v", events)
	}
	tool := Summarize(events[0])
	if tool.JobOp != "stop" || tool.JobID != "sh_20261020-014955-6adc1631" || !tool.Done {
		t.Fatalf("stop = %+v", tool)
	}
}
