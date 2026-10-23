package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/workeroutput/projection"
)

func TestSessionProjectionPreservesOverlappingTurnOffsets(t *testing.T) {
	s, id := turnStore(t)
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"{\"v\":1,\"kind\":\"message\",\"role\":\"user\",\"text\":\"hi\"}\n",
		"{\"v\":1,\"kind\":\"status\",\"name\":\"compact_boundary\"}\n",
		"{\"v\":1,\"kind\":\"message\",\"role\":\"assistant\",\"text\":\"ok\"}\n",
	}
	source := []byte(lines[0] + lines[1] + lines[2])
	path, err := s.NormalizedPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	// Turn 1 ends after the compaction line; turn 2 starts before it.
	first := int64(len(lines[0]) + len(lines[1]))
	for n, turn := range []Turn{
		{N: 1, Status: "ok", NormEnd: first},
		{N: 2, Status: "ok", NormStart: int64(len(lines[0])), NormEnd: int64(len(source))},
	} {
		raw, err := json.Marshal(turn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := saveTurnRow(t.Context(), db, id, n+1, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateOutputProjection(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, source) {
		t.Fatalf("projection changed source bytes: %v", err)
	}
	key := "sessions/" + id + "/agents/1"
	if ok, err := projection.Converted(t.Context(), db, key); err != nil || !ok {
		t.Fatalf("artifact was not checkpointed: %v", err)
	}
	var start, end int64
	var count int
	if err := db.QueryRow("SELECT count(*), min(source_start), min(source_end) FROM output_projection WHERE artifact_key=?", key).Scan(&count, &start, &end); err != nil {
		t.Fatal(err)
	}
	if count != 1 || start != int64(len(lines[0])) || end != first {
		t.Fatalf("mapping = %d rows from %d to %d; want one row for the overlapped line", count, start, end)
	}
}
