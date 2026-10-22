package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func TestRunningCacheMatchesFullRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.jsonl")
	var data bytes.Buffer
	data.WriteString("{\"kind\":\"message\",\"text\":\"previous turn\"}\n")
	turn := subagent.Turn{N: 1, Status: subagent.TurnRunning, NormStart: int64(data.Len())}
	cache := NewLoader(nil).cache("session")
	check := func() {
		t.Helper()
		if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		got, omitted := cache.read(path, turn, nil)
		limit := int64(0)
		if turn.NormEnd > 0 {
			limit = turn.NormEnd - turn.NormStart
		}
		want, _, err := runstore.ReadNormalizedOutputPath(path, turn.NormStart, limit)
		if err != nil {
			t.Fatal(err)
		}
		wantOmitted := len(want) > MaxTurnEvents
		if wantOmitted {
			want = want[len(want)-MaxTurnEvents:]
		}
		// Cache correctness is event identity and omission state. Rendering has its own tests.
		if omitted != wantOmitted || !reflect.DeepEqual(got, want) {
			t.Fatalf("incremental events differ from full read: got %d events (omitted %t), want %d (omitted %t)", len(got), omitted, len(want), wantOmitted)
		}
		if len(cache.running[1].events) > MaxTurnEvents {
			t.Fatal("unbounded cache")
		}
	}
	appendEvents := func(n int) {
		for i := 0; i < n; i++ {
			b, err := json.Marshal(workeroutput.Event{Kind: "message", Role: "assistant", Text: "output"})
			if err != nil {
				t.Fatal(err)
			}
			data.Write(b)
			data.WriteByte('\n')
		}
	}
	appendEvents(MaxTurnEvents + 17)
	data.WriteString("malformed\n")
	check()
	offset := cache.running[1].offset
	check()
	if cache.running[1].offset != offset {
		t.Fatal("unchanged file moved offset")
	}
	data.WriteString(`{"kind":"message","role":"assistant","text":"partial`)
	check()
	if cache.running[1].offset != offset {
		t.Fatal("partial record advanced offset")
	}
	data.WriteString(" complete\"}\n")
	check()
	appendEvents(MaxTurnEvents + 1)
	check()
	turn.Status, turn.NormEnd = subagent.TurnOK, int64(data.Len())
	appendEvents(3) // Later turns must not enter the completed turn.
	check()
	if len(cache.running) != 0 {
		t.Fatal("completed turn retained running cache")
	}
}

func TestRunningCacheReadsOnlyNewBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.jsonl")
	first := []byte("{\"kind\":\"message\",\"text\":\"first\"}\n")
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	cache := NewLoader(nil).cache("session")
	turn := subagent.Turn{N: 1, Status: subagent.TurnRunning}
	cache.read(path, turn, nil)
	// Overwrite the consumed prefix without changing its size. A full read
	// would lose the first event. An incremental read starts after it.
	data := append(bytes.Repeat([]byte{' '}, len(first)), []byte("{\"kind\":\"message\",\"text\":\"second\"}\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, omitted := cache.read(path, turn, nil)
	if omitted || len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("consumed prefix was read again: %+v", got)
	}
	// Unexpected truncation must discard old events and restart.
	if err := os.WriteFile(path, first, 0600); err != nil {
		t.Fatal(err)
	}
	got, omitted = cache.read(path, turn, nil)
	if omitted || len(got) != 1 || got[0].Text != "first" {
		t.Fatalf("truncation retained stale events: %+v", got)
	}
}

func TestLoaderSessionLocksAreIndependent(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261017-000001-aaaaaaaa"
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, store.Root, id, subagent.Meta{ID: id, Name: "agent", Room: "global", RoomID: "global", Created: time.Now()})
	loader := NewLoader(store)
	slow := loader.cache("another-session")
	slow.mu.Lock()
	defer slow.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := loader.Load(id, 0); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("another session's lock blocked loading")
	}
}
