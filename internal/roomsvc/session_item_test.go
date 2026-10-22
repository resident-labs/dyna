package roomsvc

import (
	"strings"
	"testing"
	"unicode/utf8"

	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

// A compaction reaches the web as its own typed item with what it reported.
func TestSessionItemCarriesCompaction(t *testing.T) {
	events := []workeroutput.Event{
		{V: workeroutput.SchemaVersion, Kind: "status", Name: "compaction", State: "started"},
		{V: workeroutput.SchemaVersion, Kind: "status", Name: "compaction", State: "completed", Trigger: "auto", TokensBefore: 182000, TokensAfter: 41000},
	}
	items, _ := transcript.FromEvents(events)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	got := sessionItem(items[0])
	if got.Kind != "compaction" || got.State != "completed" || got.Trigger != "auto" || got.TokensBefore != 182000 || got.TokensAfter != 41000 || got.Text != "" {
		t.Fatalf("item = %+v", got)
	}
}

// The DM shows folded thoughts with the same count as the CLI transcript.
func TestSessionItemKeepsThoughtCount(t *testing.T) {
	var events []workeroutput.Event
	for _, text := range []string{"First idea.", "Second idea.", "Third idea."} {
		events = append(events, workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "reasoning", State: "completed", Text: text})
	}
	items, _ := transcript.FromEvents(events)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if got := sessionItem(items[0]); got.Kind != "thought" || got.Text != "Third idea." || got.Aside != "3 thoughts" {
		t.Fatalf("item = %+v", got)
	}
}

// A failed call shows its command, exit code and error text; other calls stay light.
func TestSessionItemExplainsAFailedCall(t *testing.T) {
	exit := 1
	failed := workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "command", State: "failed", Input: `dyna post @root "hi"`, Output: "\x1b[31mError: database is locked (SQLITE_BUSY)\x1b[0m\n", Exit: &exit}
	ok := workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "command", State: "completed", Input: "ls", Output: "a.go"}
	items, _ := transcript.FromEvents([]workeroutput.Event{failed, ok})
	got := sessionItem(items[0])
	if !got.Failed || got.Command != `dyna post @root "hi"` || got.Output != "Error: database is locked (SQLITE_BUSY)" || got.Exit == nil || *got.Exit != 1 {
		t.Fatalf("failed item = %+v", got)
	}
	if light := sessionItem(items[1]); light.Command != "" || light.Output != "" || light.Exit != nil {
		t.Fatalf("a call that worked carries detail: %+v", light)
	}
	long := workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "command", State: "failed", Input: "make", Output: strings.Repeat("é", 3000)}
	items, _ = transcript.FromEvents([]workeroutput.Event{long})
	if out := sessionItem(items[0]).Output; len(out) > maxFailedOutput+len("…") || !utf8.ValidString(out) || !strings.HasPrefix(out, "…é") {
		t.Fatalf("output = %d bytes, valid %v", len(out), utf8.ValidString(out))
	}
}
