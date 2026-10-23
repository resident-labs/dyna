package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

// TestLongTurnKeepsItsCompaction reproduces session ses_20261022-021122-6ad4d278
// turn 1: two started records 30 s apart and one completion early in a two-hour
// turn, with far more events after it than the turn window holds. The divider
// must survive the window, because it is the only sign the context was compacted.
func TestLongTurnKeepsItsCompaction(t *testing.T) {
	dataDir := storetest.DataDir(t)
	path := filepath.Join(t.TempDir(), "output.jsonl")
	var data bytes.Buffer
	write := func(e workeroutput.Event) {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(b)
		data.WriteByte('\n')
	}
	write(workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "status", Name: "compaction", State: "started"})
	write(workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", Text: "Working."})
	write(workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "status", Name: "compaction", State: "started"})
	write(workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "status", Name: "compaction", State: "completed",
		Trigger: "auto", TokensBefore: 968089, TokensAfter: 5476})
	// The rest of the long turn: more events than the window keeps.
	for i := 0; i < MaxTurnEvents+50; i++ {
		write(workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", Text: "output"})
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, NormStart: 0, NormEnd: int64(data.Len())}
	events, omitted := (&sessionCache{dataDir: dataDir, key: "test-compaction", running: map[int]runningTurn{}}).read(path, turn, nil)
	if !omitted {
		t.Fatal("test turn is shorter than the window")
	}
	items, _ := FromEvents(events)
	var got []CompactionInfo
	for _, it := range items {
		if it.Kind == Compaction {
			got = append(got, it.Compaction)
		}
	}
	want := CompactionInfo{State: CompactionCompleted, Trigger: "auto", Before: 968089, After: 5476}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("compactions = %+v, want one %+v", got, want)
	}
	if items[0].Kind != Compaction {
		t.Fatalf("the divider must come before the kept tail, got %+v", items[0])
	}
}
