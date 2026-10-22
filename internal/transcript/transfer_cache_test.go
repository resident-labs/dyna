package transcript

import (
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewTransferLegacyLabelsInvalidateCache(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261017-000001-aaaaaaaa"
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	meta := subagent.Meta{ID: id, Name: "old-agent", Room: "global", RoomID: "global", Profile: "old-profile", Dir: t.TempDir(), Depth: 1, Turns: 1, Created: time.Now()}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, Ended: time.Now(), Prompt: "original task", Result: "old reply"}
	recordtest.Write(t, store.Root, id, meta)
	write(filepath.Join(dir, "turns", "1.json"), turn)
	loader := NewLoader(store)
	if _, err := loader.Load(id, 0); err != nil {
		t.Fatal(err)
	}
	// Transfer backfills these fields before selecting the new profile.
	if _, err := store.UpdateTurn(id, 1, func(t *subagent.Turn) { t.Profile = "old-profile"; t.Model = "old-model" }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateMeta(id, func(m *subagent.Meta) { m.Profile = "new-profile"; m.Model = "new-model" }); err != nil {
		t.Fatal(err)
	}
	got, err := loader.Load(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns[0].Profile != "old-profile" || got.Turns[0].Model != "old-model" {
		t.Fatalf("cached old labels lost after transfer: profile=%q model=%q current=%q", got.Turns[0].Profile, got.Turns[0].Model, got.Info.Profile)
	}
}

// A finished turn's record can change after the loader cached it: a late
// usage or end-time write, or a steering delivery flag. The next load must
// show the stored record.
func TestFinishedTurnRecordChangesInvalidateCache(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261017-000002-bbbbbbbb"
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().Add(-time.Minute)
	meta := subagent.Meta{ID: id, Name: "agent", Room: "global", RoomID: "global", Profile: "p", Dir: t.TempDir(), Depth: 1, Turns: 1, Created: start}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, Started: start, Ended: start.Add(time.Second), Prompt: "task", Result: "reply",
		Steering: []subagent.Steer{{ID: "msg_1", TS: start.UnixMilli(), Message: "steer"}}}
	recordtest.Write(t, store.Root, id, meta)
	write(filepath.Join(dir, "turns", "1.json"), turn)
	loader := NewLoader(store)
	if _, err := loader.Load(id, 0); err != nil {
		t.Fatal(err)
	}
	ended := start.Add(10 * time.Second)
	if _, err := store.UpdateTurn(id, 1, func(t *subagent.Turn) {
		t.Ended = ended
		t.Usage.OutputTokens = 42
		t.Steering[0].Delivered = true
	}); err != nil {
		t.Fatal(err)
	}
	got, err := loader.Load(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	turn1 := got.Turns[0]
	if !turn1.Ended.Equal(ended) || turn1.Usage.OutputTokens != 42 {
		t.Fatalf("cached turn kept old end or usage: ended=%v usage=%+v", turn1.Ended, turn1.Usage)
	}
	for _, it := range turn1.Items {
		if it.Kind == Steering && !it.Delivered {
			t.Fatalf("cached turn kept an old steering delivery flag: %+v", it)
		}
	}
}
