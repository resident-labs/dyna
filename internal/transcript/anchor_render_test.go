package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func TestAnchorSplitsEqualTimestampAssistantOutput(t *testing.T) {
	for _, tc := range []struct{ name, state, before, after string }{
		{"messages", "", "before", "after"},
		{"deltas", "delta", "before", "after"},
		{"repeated", "", "This answer is long enough to trigger reply deduplication.", "This answer is long enough to trigger reply deduplication."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []workeroutput.Event{
				{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", ID: "reply", State: tc.state, Text: tc.before, TS: 1},
				{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", ID: "reply", State: tc.state, Text: tc.after, TS: 1},
			}
			root := t.TempDir()
			store := &subagent.Store{Root: filepath.Join(root, "sessions")}
			id := "ses_20261023-000001-aaaaaaaa"
			dir, err := store.Dir(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "agents", "1"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
				t.Fatal(err)
			}
			recordtest.Write(t, store.Root, id, subagent.Meta{ID: id, Name: "anchor", Room: "global", RoomID: "global", Turns: 1})
			var output bytes.Buffer
			encoder := json.NewEncoder(&output)
			if err := encoder.Encode(events[0]); err != nil {
				t.Fatal(err)
			}
			anchor := int64(output.Len())
			if err := encoder.Encode(events[1]); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "agents", "1", "output.jsonl"), output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			turn := subagent.Turn{N: 1, Status: subagent.TurnOK, NormEnd: int64(output.Len()), Prompt: "task", Steering: []subagent.Steer{{Message: "check", TS: 1, Delivered: true, SourceOffset: &anchor}}}
			raw, err := json.Marshal(turn)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := NewLoader(store).LoadTurn(id, 1)
			if err != nil {
				t.Fatal(err)
			}
			items := loaded.Turns[0].Items
			if len(items) != 4 || items[1].Text != tc.before || items[2].Kind != Steering || items[3].Text != tc.after {
				t.Fatalf("anchor did not split assistant output: %+v", items)
			}
		})
	}
}
